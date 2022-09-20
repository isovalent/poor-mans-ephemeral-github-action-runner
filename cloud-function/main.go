package p

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	ghinstallation "github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v45/github"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

const (
	ghRunnerURL = "https://github.com/actions/runner/releases/download/v2.295.0/actions-runner-linux-x64-2.295.0.tar.gz"
	ghRunnerSum = "a80c1ab58be3cd4920ac2e51948723af33c2248b434a8a20bd9b3891ca4000b6"

	project     = "cilium-ci-gh-temp"
	zone        = "us-central1-a"
	machineType = "projects/" + project + "/zones/" + zone + "/machineTypes/n1-standard-4"
)

var (
	ghRepos             []string
	ghWebhookToken      string
	ghAppPrivKeyPath    string
	ghAppID             int
	ghAppInstallationID int

	gcpCredentialsPath string
	gcpVMTTL           time.Duration
	gcpGCAuthToken     string
)

func init() {
	var err error

	ghRepos = strings.Split(os.Getenv("GH_REPOS"), ",")
	if len(ghRepos) == 0 {
		panic("GH_REPOS is not set")
	}

	// Accessing it from GCP Secrets appends the newline
	ghWebhookToken = strings.TrimSuffix(os.Getenv("GH_WEBHOOK_TOKEN"), "\n")

	id := os.Getenv("GH_APP_ID")
	ghAppID, err = strconv.Atoi(id)
	if err != nil {
		panic(fmt.Sprintf("failed to convert GH_APP_ID to int: val=%s err=%s", id, err))
	}

	id = os.Getenv("GH_APP_INSTALLATION_ID")
	ghAppInstallationID, err = strconv.Atoi(id)
	if err != nil {
		panic(fmt.Sprintf("failed to convert GH_APP_INSTALLATION_ID to int: val=%s err=%s", id, err))
	}

	ghAppPrivKeyPath = os.Getenv("GH_APP_PRIV_KEY_PATH")
	if len(ghAppPrivKeyPath) == 0 {
		panic("GH_APP_PRIV_KEY_PATH is not specified")
	}

	gcpCredentialsPath = os.Getenv("GCP_CREDENTIALS_PATH")

	ttl := os.Getenv("GCP_VM_TTL")
	if ttl == "" {
		ttl = "2h"
	}
	gcpVMTTL, err = time.ParseDuration(ttl)
	if err != nil {
		panic(fmt.Sprintf("failed to parse GCP_VM_TTL: val=%s err=%s", ttl, err))
	}

	gcpGCAuthToken = os.Getenv("GCP_GC_AUTH_TOKEN")
}

// registerRunner creates a new token for a Github runner. The token is going to be
// consumed by a to-be started VM.
func registerRunner(repo string) (string, error) {
	ctx := context.Background()

	itr, err := ghinstallation.NewKeyFromFile(http.DefaultTransport,
		int64(ghAppID), int64(ghAppInstallationID), ghAppPrivKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to auth: %w", err)
	}

	client := github.NewClient(&http.Client{Transport: itr})

	repoInfo := strings.Split(repo, "/")
	token, _, err := client.Actions.CreateRegistrationToken(ctx, repoInfo[0], repoInfo[1])
	if err != nil {
		return "", fmt.Errorf("failed to create runner token: %w", err)
	}

	return *token.Token, nil
}

func newComputeService() (*compute.Service, error) {
	ctx := context.Background()
	if gcpCredentialsPath != "" {
		return compute.NewService(ctx, option.WithCredentialsFile(gcpCredentialsPath))
	}
	return compute.NewService(ctx)
}

func getVMName(id int64) string {
	return fmt.Sprintf("cilium-ci-gh-ephemeral-runner-%d", id)
}

func createVM(id int64, ghRunnerToken string, repo string) error {
	c, err := newComputeService()
	if err != nil {
		return fmt.Errorf("failed to initialize new service: %w", err)
	}

	script := `#!/bin/sh
apt-get update
apt-get install -y jq docker.io golang-go qemu-system-x86

LOG_FILE=/tmp/action-runner.log
mkdir actions-runner && cd actions-runner
echo "gh-starting" >> ${LOG_FILE}
curl -o actions-runner.tar.gz -L %s >> ${LOG_FILE}
echo "gh-downloaded" >> ${LOG_FILE}
#echo "%s actions-runner.tar.gz" | shasum -a 256 -c
tar xzf ./actions-runner.tar.gz
echo "gh-configuring" >> ${LOG_FILE}
RUNNER_ALLOW_RUNASROOT=1 ./config.sh --url https://github.com/%s --token %s --ephemeral --unattended >> ${LOG_FILE}
echo "gh-configured" >> ${LOG_FILE}
RUNNER_ALLOW_RUNASROOT=1 ./run.sh >> ${LOG_FILE}
echo "gh-done" >> ${LOG_FILE}
`
	script = fmt.Sprintf(script, ghRunnerURL, ghRunnerSum, repo, ghRunnerToken)
	vmName := getVMName(id)

	instance := &compute.Instance{
		Name: vmName,
		Description: fmt.Sprintf("Cilium CI GH ephemeral runner VM for repo %q and workjob id %d",
			repo, id),
		MachineType:    machineType,
		MinCpuPlatform: "Intel Haswell",
		AdvancedMachineFeatures: &compute.AdvancedMachineFeatures{
			EnableNestedVirtualization: true,
		},
		NetworkInterfaces: []*compute.NetworkInterface{
			{
				Network: fmt.Sprintf("projects/%s/global/networks/default", project),
				AccessConfigs: []*compute.AccessConfig{
					{Type: "ONE_TO_ONE_NAT", Name: "External NAT"},
				},
			},
		},
		Disks: []*compute.AttachedDisk{
			{
				AutoDelete: true,
				Boot:       true,
				Type:       "PERSISTENT",
				InitializeParams: &compute.AttachedDiskInitializeParams{
					DiskName:    vmName,
					DiskType:    "projects/" + project + "/zones/" + zone + "/diskTypes/pd-balanced",
					SourceImage: "projects/ubuntu-os-cloud/global/images/ubuntu-2204-jammy-v20220810",
					DiskSizeGb:  30,
				},
			},
		},
		Metadata: &compute.Metadata{
			Items: []*compute.MetadataItems{
				{
					Key:   "startup-script",
					Value: &script,
				},
			},
		},
	}
	if _, err := c.Instances.Insert(project, zone, instance).Do(); err != nil {
		return fmt.Errorf("failed to create VM: vmname=%s err=%w", vmName, err)
	}

	log.Printf("created VM: vmname=%s\n", vmName)

	return nil
}

func deleteVM(vmName string) error {
	c, err := newComputeService()
	if err != nil {
		return fmt.Errorf("failed to initialize new service: %w", err)
	}

	if _, err := c.Instances.Delete(project, zone, vmName).Do(); err != nil {
		return fmt.Errorf("failed to delete VM: vmname=%s err=%w", vmName, err)
	}

	log.Printf("deleted VM: vmname=%s\n", vmName)

	return nil
}

func isAllowedRepo(repo string) bool {
	for _, r := range ghRepos {
		if r == repo {
			return true
		}
	}
	return false
}

func handleWorkflowJobEvent(e *github.WorkflowJobEvent) error {
	if e.Repo == nil {
		return fmt.Errorf("repo is not set")
	}

	repo := *(e.Repo.FullName)

	if !isAllowedRepo(repo) {
		return nil
	}

	if e.Action == nil || (*(e.Action) != "queued" && *(e.Action) != "completed") {
		return nil
	}

	if e.WorkflowJob == nil || e.WorkflowJob.ID == nil {
		return fmt.Errorf("invalid workflow_job payload: %v", e)
	}

	isSelfHosted := false
	for _, label := range e.WorkflowJob.Labels {
		if label == "self-hosted" {
			isSelfHosted = true
			break
		}
	}
	if !isSelfHosted {
		// No "self-hosted" runner is required, so nothing to do
		return nil
	}

	log.Printf("Handling workflow job: id=%d\n", *e.WorkflowJob.ID)

	switch *(e.Action) {
	case "queued":
		token, err := registerRunner(repo)
		if err != nil {
			return fmt.Errorf("failed to register runner: %w", err)
		}

		if err := createVM(*e.WorkflowJob.ID, token, repo); err != nil {
			return fmt.Errorf("failed to create VM: %w", err)
		}
	case "completed":
		if e.WorkflowJob.RunnerName != nil {
			if err := deleteVM(*e.WorkflowJob.RunnerName); err != nil {
				return fmt.Errorf("failed to delete VM: %w", err)
			}
		}
	}

	return nil
}

func HandleGithubEvents(w http.ResponseWriter, r *http.Request) {
	payload, err := github.ValidatePayload(r, []byte(ghWebhookToken))
	if err != nil {
		log.Printf("error validating request: err=%s\n", err)
		w.WriteHeader(401)
		return
	}
	defer r.Body.Close()

	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		log.Printf("could not parse webhook: err=%s\n", err)
		w.WriteHeader(400)
		return
	}

	switch e := event.(type) {
	case *github.PingEvent:
		w.WriteHeader(200)
		return
	case *github.WorkflowJobEvent:
		if err := handleWorkflowJobEvent(e); err != nil {
			log.Printf("failed to handle workflow_job event: err=%s\n", err)
			w.WriteHeader(400)
			return
		}
	default:
		log.Printf("not supported event type: type=%s\n", github.WebHookType(r))
		w.WriteHeader(400)
		return
	}

	w.WriteHeader(200)
}

func gcInstances() error {
	c, err := newComputeService()
	if err != nil {
		return fmt.Errorf("failed to initialize new service: %w", err)
	}

	res, err := c.Instances.AggregatedList(project).Do()
	if err != nil {
		return fmt.Errorf("failed to list instances: err=%w", err)
	}

	for zone, instances := range res.Items {
		zone := strings.TrimPrefix(zone, "zones/")
		for _, instance := range instances.Instances {
			t, err := time.Parse(time.RFC3339, instance.CreationTimestamp)
			if err != nil {
				log.Printf("failed to parse instance creation time: instance=%s, ts=%s",
					instance.Name, instance.CreationTimestamp)
				continue
			}

			t = t.Add(gcpVMTTL)
			if time.Now().Before(t) {
				continue
			}

			log.Printf("deleting stale VM: vmname=%s, creation-time=%s",
				instance.Name, instance.CreationTimestamp)
			if _, err := c.Instances.Delete(project, zone, instance.Name).Do(); err != nil {
				log.Printf("failed to delete VM: vmname=%s err=%s", instance.Name, err)
			}
		}
	}

	return nil

}

func HandleGC(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Printf("error reading body: %v", err)
		http.Error(w, "can't read body", http.StatusBadRequest)
		return
	}

	// poor man's auth
	if string(body) != gcpGCAuthToken {
		log.Printf("failed to auth (token mismatch)")
		w.WriteHeader(400)
		return
	}

	if err := gcInstances(); err != nil {
		log.Printf("failed to handle GC request: err=%s", err)
		w.WriteHeader(400)
		return
	}

	w.WriteHeader(200)
}

func main() {
	listenOn := ":8090"
	log.Printf("Starting to listen on %s for GH_REPOS=%v GH_APP_ID=%d GH_APP_INSTALLATION_ID=%d\n",
		listenOn, ghRepos, ghAppID, ghAppInstallationID)

	http.HandleFunc("/payload", HandleGithubEvents)
	http.HandleFunc("/gc", HandleGC)
	http.ListenAndServe(listenOn, nil)
}
