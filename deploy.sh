#!/usr/bin/env bash
# Deploy shopsync as a Cloud Run Job triggered daily by Cloud Scheduler.
# Usage: ./deploy.sh [COMMAND] [--project PROJECT_ID] [--region REGION]
#
# Commands:
#   build      Submit Cloud Build only
#   deploy     Deploy/update Cloud Run Job only
#   scheduler  Configure IAM + Cloud Scheduler only
#   all        Run build + deploy + scheduler (default)
set -euo pipefail

PROJECT="${GCP_PROJECT:-}"
REGION="${GCP_REGION:-us-central1}"
JOB_NAME="shopsync"
IMAGE_REPO="shopsync"
SCHEDULE="0 7 * * *"  # 07:00 UTC daily
WP_URL="https://theimprovshop.com/wp-json/tribe/events/v1/events"
SECRET_NAME="IMPROVWIKI_DB_URL"

COMMAND="all"

# Pull off the command if it's not a flag
if [[ $# -gt 0 && "$1" != --* ]]; then
  COMMAND="$1"
  shift
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --project) PROJECT="$2"; shift 2 ;;
    --region)  REGION="$2";  shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

if [[ -z "$PROJECT" ]]; then
  PROJECT=$(gcloud config get-value project 2>/dev/null)
  if [[ -z "$PROJECT" ]]; then
    echo "ERROR: set GCP_PROJECT or pass --project <project-id>"
    exit 1
  fi
fi

IMAGE="$REGION-docker.pkg.dev/$PROJECT/$IMAGE_REPO/$JOB_NAME:latest"

echo "==> Project:  $PROJECT"
echo "==> Region:   $REGION"
echo "==> Image:    $IMAGE"
echo ""

cmd_build() {
  echo "==> Submitting build to Cloud Build..."
  gcloud builds submit \
    --tag "$IMAGE" \
    --project "$PROJECT" \
    .
}

cmd_deploy() {
  echo "==> Deploying Cloud Run Job '$JOB_NAME'..."
  gcloud run jobs deploy "$JOB_NAME" \
    --image "$IMAGE" \
    --region "$REGION" \
    --project "$PROJECT" \
    --set-secrets "DATABASE_URL=${SECRET_NAME}:latest" \
    --args="-wp,$WP_URL,-dry-run=false" \
    --max-retries 3 \
    --task-timeout 5m
}

cmd_scheduler() {
  SCHEDULER_SA="$SA_EMAIL"
  echo "==> Granting run.invoker to $SCHEDULER_SA ..."
  gcloud projects add-iam-policy-binding "$PROJECT" \
    --member="serviceAccount:$SCHEDULER_SA" \
    --role="roles/run.invoker"

  echo "==> Configuring Cloud Scheduler (${SCHEDULE} UTC)..."
  if gcloud scheduler jobs describe "$JOB_NAME-daily" \
    --location "$REGION" --project "$PROJECT" &>/dev/null; then
    gcloud scheduler jobs update http "$JOB_NAME-daily" \
      --location "$REGION" \
      --project "$PROJECT" \
      --schedule "$SCHEDULE" \
      --time-zone "UTC" \
      --uri "https://$REGION-run.googleapis.com/apis/run.googleapis.com/v1/namespaces/$PROJECT/jobs/$JOB_NAME:run" \
      --oauth-service-account-email "$SCHEDULER_SA" \
      --message-body "{}"
  else
    gcloud scheduler jobs create http "$JOB_NAME-daily" \
      --location "$REGION" \
      --project "$PROJECT" \
      --schedule "$SCHEDULE" \
      --time-zone "UTC" \
      --uri "https://$REGION-run.googleapis.com/apis/run.googleapis.com/v1/namespaces/$PROJECT/jobs/$JOB_NAME:run" \
      --oauth-service-account-email "$SCHEDULER_SA" \
      --message-body "{}"
  fi
}

case "$COMMAND" in
  build)     cmd_build ;;
  deploy)    cmd_deploy ;;
  scheduler) cmd_scheduler ;;
  all)
    cmd_build
    cmd_deploy
    cmd_scheduler
    echo ""
    echo "Done! Run manually any time with:"
    echo "  gcloud run jobs execute $JOB_NAME --region $REGION --project $PROJECT"
    echo ""
    echo "Trigger immediately via Scheduler:"
    echo "  gcloud scheduler jobs run $JOB_NAME-daily --location $REGION --project $PROJECT"
    ;;
  *) echo "Unknown command: $COMMAND (use build, deploy, scheduler, or all)"; exit 1 ;;
esac
