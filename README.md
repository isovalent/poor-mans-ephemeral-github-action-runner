# Poor Man's Ephemeral Github Action Runner

## TODO

- [ ] Schedule VM removal in case we miss GH Webhook "completed" event.

## HOWTO

### Create Github App

This step is no longer needed, as the GH App was created:
https://github.com/apps/cilium-gh-ephemeral-runner-tokens.

- Go to https://github.com/settings/apps/new
- Create a new app with the following permission settings:
    - `Actions` (read)
    - `Administration` (read / write)
    - `Metadata` (read)
- Store the App ID (`GH_APP_ID`).
- Generate a new private key (`GH_APP_PRIV_KEY_PATH`).
- Install the app and store the installation ID (`GH_APP_INSTALLATION_ID`).

### Create Cloud Function

- Create a random `GH_WEBHOOK_TOKEN`.
- Create the following secrets:
    - `gcloud secrets create "ci_gh_webhook_token" --replication-policy "automatic" --data-file - <<< "${GH_WEBHOOK_TOKEN}"`
    - `gcloud secrets create "ci_gh_app_priv_key" --replication-policy "automatic" --data-file "${GH_APP_PRIV_KEY_PATH}"`
- Deploy Clound Function with:
  ```
  gcloud functions deploy HandleGithubEvents \
    --runtime go116 --trigger-http --allow-unauthenticated \
    --set-env-vars=GH_APP_ID=${GH_APP_ID},GH_APP_INSTALLATION_ID=${GH_APP_INSTALLATION_ID},GH_REPO="cilium/cilium",GH_APP_PRIV_KEY_PATH=/secrets/ci_gh_app_priv_key \
    --set-secrets=GH_WEBHOOK_TOKEN=ci_gh_webhook_token:latest,/secrets/ci_gh_app_priv_key=ci_gh_app_priv_key:latest
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
  GH_REPO=cilium/cilium \
  GCP_CREDENTIALS_PATH=${GCP_CREDENTIALS_PATH} \
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
