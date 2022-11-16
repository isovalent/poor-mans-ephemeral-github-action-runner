# Poor Man's Ephemeral Github Action Runner

## HOWTO

### Create Github App

This step is no longer needed, as the GH App was created:
https://github.com/apps/cilium-gh-ephemeral-runner-tokens.

- Go to https://github.com/settings/apps/new
- Create a new app with the following permission settings:
    - `Actions` (read)
    - `Metadata` (read)
    - For `Repository`: `Administration` (read / write)
    - For `Organization`: `Self Hosted Runners` (read / write)
- Store the App ID (`GH_APP_ID`).
- Generate a new private key (`GH_APP_PRIV_KEY_PATH`).
- Install the app and store the installation ID (`GH_APP_INSTALLATION_ID`).

### Create Cloud Functions and Cloud Scheduler

- Create a random `GH_WEBHOOK_TOKEN`.
- Create the following secrets:
    - `gcloud secrets create "ci_gh_webhook_token" --replication-policy "automatic" --data-file - <<< "${GH_WEBHOOK_TOKEN}"`
    - `gcloud secrets create "ci_gh_app_priv_key" --replication-policy "automatic" --data-file "${GH_APP_PRIV_KEY_PATH}"`
- Set variables:
  ```
  GH_APP_ID=231399
  GH_APP_INSTALLATION_ID=28541842
  GH_RUNNER_URL="https://github.com/actions/runner/releases/download/v2.299.1/actions-runner-linux-x64-2.299.1.tar.gz"
  GH_RUNNER_SUM="147c14700c6cb997421b9a239c012197f11ea9854cd901ee88ead6fe73a72c74"
  ```
- Deploy Cloud Function to schedule VMs with:
  ```
  gcloud functions deploy HandleGithubEvents \
    --runtime go116 --trigger-http --allow-unauthenticated \
    --set-env-vars="^;^GH_APP_ID=${GH_APP_ID};GH_APP_INSTALLATION_ID=${GH_APP_INSTALLATION_ID};GH_REPOS=cilium/cilium,cilium/tetragon,cilium/pwru;GH_APP_PRIV_KEY_PATH=/secrets/ci_gh_app_priv_key;GH_RUNNER_URL=${GH_RUNNER_URL};GH_RUNNER_SUM=${GH_RUNNER_SUM}" \
    --set-secrets=GH_WEBHOOK_TOKEN=ci_gh_webhook_token:latest,/secrets/ci_gh_app_priv_key=ci_gh_app_priv_key:latest
  ```
- Deploy Cloud Function to GC stale VMs:
  ```
  gcloud functions deploy HandleGC --runtime go116 --trigger-http --allow-unauthenticated \
     --set-env-vars GH_APP_ID=42,GH_APP_INSTALLATION_ID=42,GH_REPOS=notused,GH_APP_PRIV_KEY_PATH=notused,GH_WEBHOOK_TOKEN=notused,GCP_VM_TTL=2h,GCP_GC_AUTH_TOKEN=${GCP_GC_AUTH_TOKEN}
  ```
- Create Cloud Scheduler to start the GC every 10 minutes:
```
gcloud scheduler jobs create http gc-stale-gh-action-vms \
    --location us-central1-a \
    --schedule "*/10 * * * *" \
    --uri "${CLOUD_FUNCTION_GC_URL}" \
    --http-method POST \
    --message-body "${GCP_GC_AUTH_TOKEN}"
```


### Run locally (optional)

- `cd cloud-function`
- `sed -i 's/package p/package main/' main.go`
- Start the server with:
  ```
  GH_WEBHOOK_TOKEN=${GH_WEBHOOK_TOKEN} \
  GH_APP_ID=${GH_APP_ID} \
  GH_APP_INSTALLATION_ID=${GH_APP_INSTALLATION_ID} \
  GH_APP_PRIV_KEY_PATH=${GH_APP_PRIV_KEY_PATH} \
  GH_REPOS=cilium/cilium \
  GCP_CREDENTIALS_PATH=${GCP_CREDENTIALS_PATH} \
  GH_RUNNER_URL=${GH_RUNNER_URL} \
  GH_RUNNER_SUM=${GH_RUNNER_SUM} \
  go run ./main.go
  ```
- Expose the server to the internet with `ngrok http 8090` (use the exposed URL when
  creating the Github Webhook below).

### Create Github Webhook

- Create Webhook at https://github.com/cilium/cilium/settings/hooks with
  the following settings:
    - `Payload URL` set to the Cloud Function URL from above.
    - `Content Type` set to `application/json`
    - `Secret` set to `${GH_WEBHOOK_TOKEN}]`
    - Select the individual event `Workflow jobs`.

### Start using

- Create a GH workflow with `runner: self-hosted`.
