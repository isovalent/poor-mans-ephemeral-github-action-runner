# Poor Man's Ephemeral Github Action Runner

## TODO

- [ ] Schedule VM removal in case we miss GH Webhook "completed" event.

## HOWTO

- Create Github API Token with `admin:org`, `repo` and `workflow` permissions
  at https://github.com/settings/tokens.
- Deploy GCP Clound Function with `gcloud functions deploy HandleGithubEvents
    --runtime go116 --trigger-http --allow-unauthenticated
    --set-env-vars=GH_API_TOKEN=$GH_API_TOKEN,GH_WEBHOOK_TOKEN=$GH_WEBHOOK_TOKEN`.
- Create Webhook at https://github.com/cilium/cilium/settings/hooks with
  `Payload URL` set to the Cloud Function URL from above, `Content Type` set to
  `application/json`, `Secret` set to `$GH_WEBHOOK_TOKEN`, and select the
  individual event `Workflow jobs`.
- Create a GH workflow with `runner: self-hosted`.
