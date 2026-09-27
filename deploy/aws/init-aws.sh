#!/usr/bin/env bash
# Provisions one wager FIFO queue per provider, the shared DLQ, the events topic and IAM
# policies. Safe to run repeatedly.
#
# LocalStack Community does not evaluate IAM. Each producer policy allows SendMessage on
# only that provider's queue; the application enforces the same binding when it reads.
set -euo pipefail

account_id="${AWS_ACCOUNT_ID:-000000000000}"
region="${AWS_DEFAULT_REGION:-us-east-1}"

queue_arn() {
  aws sqs get-queue-attributes --queue-url "$1" \
    --attribute-names QueueArn --query Attributes.QueueArn --output text
}

dlq_url=$(aws sqs create-queue --queue-name wager-transactions-dlq.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false,MessageRetentionPeriod=1209600 \
  --query QueueUrl --output text)
dlq_arn=$(queue_arn "$dlq_url")

# Must match config.DefaultQueueVisibilityTimeout (SQS_QUEUE_VISIBILITY_TIMEOUT default).
visibility_timeout="${SQS_QUEUE_VISIBILITY_TIMEOUT:-60}"

# Attributes are (re)applied separately so an existing queue converges to this config.
apply_wager_attributes() {
  aws sqs set-queue-attributes --queue-url "$1" --attributes "$(cat <<JSON
{
  "VisibilityTimeout": "${visibility_timeout}",
  "ReceiveMessageWaitTimeSeconds": "20",
  "MessageRetentionPeriod": "345600",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"${dlq_arn}\",\"maxReceiveCount\":\"5\"}"
}
JSON
)"
}

# The previous shared queue let every producer send as any provider. Drop it when it is still there.
legacy=$(aws sqs get-queue-url --queue-name wager-transactions.fifo --query QueueUrl --output text 2>/dev/null || true)
if [ -n "$legacy" ] && [ "$legacy" != "None" ]; then
  aws sqs delete-queue --queue-url "$legacy"
fi

for provider in provider-a provider-b; do
  wager_url=$(aws sqs create-queue --queue-name "wager-transactions-${provider}.fifo" \
    --attributes FifoQueue=true,ContentBasedDeduplication=false \
    --query QueueUrl --output text)
  apply_wager_attributes "$wager_url"
  echo "wager queue ${provider}: ${wager_url}"
done

topic_arn=$(aws sns create-topic --name wallet-events.fifo \
  --attributes FifoTopic=true,ContentBasedDeduplication=false \
  --query TopicArn --output text)

audit_url=$(aws sqs create-queue --queue-name wallet-events-audit.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false \
  --query QueueUrl --output text)
audit_arn=$(queue_arn "$audit_url")

aws sqs set-queue-attributes --queue-url "$audit_url" --attributes "$(cat <<JSON
{
  "Policy": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"Service\":\"sns.amazonaws.com\"},\"Action\":\"sqs:SendMessage\",\"Resource\":\"${audit_arn}\",\"Condition\":{\"ArnEquals\":{\"aws:SourceArn\":\"${topic_arn}\"}}}]}"
}
JSON
)"

existing_sub=$(aws sns list-subscriptions-by-topic --topic-arn "$topic_arn" \
  --query "Subscriptions[?Endpoint=='${audit_arn}'].SubscriptionArn" --output text)
if [ -z "$existing_sub" ] || [ "$existing_sub" = "None" ]; then
  aws sns subscribe --topic-arn "$topic_arn" --protocol sqs \
    --notification-endpoint "$audit_arn" \
    --attributes RawMessageDelivery=true >/dev/null
fi

render() {
  sed -e "s/\${ACCOUNT_ID}/${account_id}/g" -e "s/\${REGION}/${region}/g" "$1"
}

for file in /init/policies/*.json; do
  name=$(basename "$file" .json)
  if ! aws iam get-policy --policy-arn "arn:aws:iam::${account_id}:policy/${name}" >/dev/null 2>&1; then
    aws iam create-policy --policy-name "$name" --policy-document "$(render "$file")" >/dev/null
  fi
done

for user in wallet-consumer provider-a-producer provider-b-producer; do
  aws iam get-user --user-name "$user" >/dev/null 2>&1 || aws iam create-user --user-name "$user" >/dev/null
done
aws iam attach-user-policy --user-name wallet-consumer \
  --policy-arn "arn:aws:iam::${account_id}:policy/wallet-consumer"
aws iam attach-user-policy --user-name provider-a-producer \
  --policy-arn "arn:aws:iam::${account_id}:policy/provider-a-producer"
aws iam attach-user-policy --user-name provider-b-producer \
  --policy-arn "arn:aws:iam::${account_id}:policy/provider-b-producer"

old_producer="arn:aws:iam::${account_id}:policy/provider-producer"
for user in provider-a-producer provider-b-producer; do
  attached=$(aws iam list-attached-user-policies --user-name "$user" \
    --query "AttachedPolicies[?PolicyArn=='${old_producer}'].PolicyArn" --output text)
  if [ -n "$attached" ] && [ "$attached" != "None" ]; then
    aws iam detach-user-policy --user-name "$user" --policy-arn "$old_producer"
  fi
done

# Each run rotates the keys, so the credentials file always matches IAM.
credentials_file="${CREDENTIALS_FILE:-/credentials/credentials}"
mkdir -p "$(dirname "$credentials_file")"
tmp_credentials="${credentials_file}.tmp"
: > "$tmp_credentials"
for user in wallet-consumer provider-a-producer provider-b-producer; do
  for key in $(aws iam list-access-keys --user-name "$user" --query 'AccessKeyMetadata[].AccessKeyId' --output text); do
    aws iam delete-access-key --user-name "$user" --access-key-id "$key"
  done
  read -r key_id secret < <(aws iam create-access-key --user-name "$user" \
    --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text)
  printf '[%s]\naws_access_key_id = %s\naws_secret_access_key = %s\n\n' "$user" "$key_id" "$secret" >> "$tmp_credentials"
done
chmod 0644 "$tmp_credentials"
mv "$tmp_credentials" "$credentials_file"

echo "credentials:  ${credentials_file}"
echo "wager dlq:    ${dlq_url}"
echo "events topic: ${topic_arn}"
echo "audit queue:  ${audit_url}"
