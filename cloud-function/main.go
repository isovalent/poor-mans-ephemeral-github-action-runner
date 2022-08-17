package p

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/google/go-github/v45/github"
	"golang.org/x/oauth2"
	compute "google.golang.org/api/compute/v1"
)

const (
	ghRepo      = "brb/cilium"
	ghRunnerURL = "https://github.com/actions/runner/releases/download/v2.295.0/actions-runner-linux-x64-2.295.0.tar.gz"
	ghRunnerSum = "a80c1ab58be3cd4920ac2e51948723af33c2248b434a8a20bd9b3891ca4000b6"

	project     = "cilium-dev"
	zone        = "europe-west1-b"
	machineType = "projects/" + project + "/zones/" + zone + "/machineTypes/n1-standard-4"
)

var (
	ghAPIToken      string
	ghWebhookSecret string
)

func init() {
	ghAPIToken = os.Getenv("GH_API_TOKEN")
	ghWebhookSecret = os.Getenv("GH_WEBHOOK_SECRET")
}

func registerRunner() (string, error) {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: ghAPIToken},
	)
	tc := oauth2.NewClient(ctx, ts)

	client := github.NewClient(tc)

	token, _, err := client.Actions.CreateRegistrationToken(ctx, "brb", "cilium")
	if err != nil {
		return "", fmt.Errorf("failed to create runner token: %w", err)
	}

	return *token.Token, nil
}

func createVM(id int64, ghRunnerToken string) error {
	ctx := context.Background()
	c, err := compute.NewService(ctx)
	//c, err := compute.NewService(ctx, option.WithCredentialsFile("/home/brb/Downloads/gcp.json"))
	if err != nil {
		return fmt.Errorf("failed to initialize new service: %w", err)
	}

	script := `#!/bin/sh
mkdir actions-runner && cd actions-runner
echo "gh-starting" >> /tmp/log
curl -o actions-runner.tar.gz -L %s >> /tmp/log
echo "gh-downloaded" >> /tmp/log
#echo "%s actions-runner.tar.gz" | shasum -a 256 -c
tar xzf ./actions-runner.tar.gz
echo "configuring" >> /tmp/log
RUNNER_ALLOW_RUNASROOT=1 ./config.sh --url https://github.com/%s --token %s --ephemeral --unattended >> /tmp/log
echo "configured" >> /tmp/log
RUNNER_ALLOW_RUNASROOT=1 ./run.sh >> /tmp/log
echo "done" >> /tmp/log
#sudo shutdown -h now
`
	script = fmt.Sprintf(script, ghRunnerURL, ghRunnerSum, ghRepo, ghRunnerToken)
	vmName := fmt.Sprintf("martynas-hj-%d", id)

	instance := &compute.Instance{
		Name:        vmName,
		Description: "TODO",
		MachineType: machineType,
		NetworkInterfaces: []*compute.NetworkInterface{
			// TODO: something more secure
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
		return fmt.Errorf("failed to create VM: name=%s err=%w", vmName, err)
	}

	log.Printf("created VM: %s\n", vmName)

	return nil
}

func deleteVM(id int64) error {
	ctx := context.Background()
	c, err := compute.NewService(ctx)
	//c, err := compute.NewService(ctx, option.WithCredentialsFile("/home/brb/Downloads/gcp.json"))
	if err != nil {
		return fmt.Errorf("failed to initialize new service: %w", err)
	}

	vmName := fmt.Sprintf("martynas-hj-%d", id)

	if _, err := c.Instances.Delete(project, zone, vmName).Do(); err != nil {
		return fmt.Errorf("failed to delete VM: name=%s err=%w", vmName, err)
	}

	log.Printf("deleted VM: %s\n", vmName)

	return nil
}

func handleWorkflowJobEvent(e *github.WorkflowJobEvent) error {
	if e.Repo == nil || *(e.Repo.FullName) != ghRepo {
		return fmt.Errorf("invalid repo (%s)", e.Repo)
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

	switch *(e.Action) {
	case "queued":
		token, err := registerRunner()
		if err != nil {
			return fmt.Errorf("failed to register runner: %w", err)
		}

		if err := createVM(*e.WorkflowJob.ID, token); err != nil {
			return fmt.Errorf("failed to create VM: %w", err)
		}
	case "completed":
		if err := deleteVM(*e.WorkflowJob.ID); err != nil {
			return fmt.Errorf("failed to delete VM: %w", err)
		}
	}

	// "completed"

	return nil
}

func HandleGithubEvents(w http.ResponseWriter, r *http.Request) {
	payload, err := github.ValidatePayload(r, []byte(ghWebhookSecret))
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
	return
}

func main() {
	http.HandleFunc("/payload", HandleGithubEvents)
	http.ListenAndServe(":8090", nil)
}
