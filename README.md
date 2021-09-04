# gcp-iap-token-writer
An app to retrieve Google Cloud Platform Bearer auth token useable by Google Service Accounts for authenticating through GCP Identity Aware Proxy (IAP).

## Usage


> NOTE: This application is _*not intended*_  for end users. For authenticating as an end user to an application, you can use `gcloud auth print-access-token`. Using this application as a user or without Service Account credentials will not work.

Assuming you have downloaded GCP Service Account credentials to a file named `gcp.json`:

```bash
export GOOGLE_APPLICATION_CREDENTIALS=gcp.json 
gcp-iap-token-writer \
    --audience=some-audience.apps.googleusercontent.com \
    --filepath=gcptoken
```

This will request, sign, and store to a local file a GCP Access Token. The token will be signed with the provided IAP audience as a custom claim, which should be the audience which the serving application you are authenticating to is configured to use.

After the application starts, you can use the access token in an application for automatically authenticating requests to the IAP protected endpoint.

For example:
```
curl -H "Authorization: Bearer $(cat gcptoken)" https://your-iap-protected-service.example.com/api/v1
```
