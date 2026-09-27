# Jungle Wallet

Serviço em Go, composto com Uber Fx, que movimenta carteiras de jogadores a partir de operações
de provedores de jogos (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`) recebidas por HTTP ou por uma
fila SQS FIFO. O estado financeiro fica no PostgreSQL, os eventos de integração saem por uma
outbox transacional para um tópico SNS FIFO e a autenticação é feita com tokens OIDC emitidos
pelo Keycloak.

As decisões de projeto estão em [ARCHITECTURE.md](ARCHITECTURE.md).

## Garantia principal

Uma operação financeira aceita nunca é perdida nem aplicada duas vezes:

- saldo, lançamento no ledger, estado da transação, registro da inbox (quando veio pelo SQS) e
  eventos da outbox são gravados no **mesmo commit**;
- a mensagem SQS só é removida da fila **depois** desse commit; se o processo cair antes, a
  mensagem é reentregue e a inbox e a idempotência transformam a reentrega em replay;
- eventos só são publicados a partir da outbox, **depois** do commit, e nunca são descartados:
  falhas voltam para a fila da outbox com backoff;
- operações que esperam uma referência ficam persistidas como `PENDING_REFERENCE` e são
  retomadas por qualquer instância, inclusive depois de um reinício.

Os detalhes estão em [ARCHITECTURE.md](ARCHITECTURE.md#garantias-e-como-são-obtidas).

## Pré-requisitos

- Docker com Docker Compose v2.
- Go 1.25 (a versão exata está em `go.mod` e no `Dockerfile`), para rodar testes e comandos locais.
- `curl` e `jq` para os exemplos abaixo; AWS CLI é opcional (para enviar mensagens SQS do host).
- Portas livres: `5432` (PostgreSQL), `8080` (Keycloak), `4566` (LocalStack), `8081`-`8083` (API)
  e `9091`-`9093` (métricas).

## Início rápido

```sh
cp .env.example .env        # opcional: o compose já carrega .env.example
docker compose up --build -d
docker compose ps
```

O compose sobe, nesta ordem:

| Serviço | Papel |
| --- | --- |
| `postgres` | PostgreSQL 17; `deploy/postgres/init` cria os papéis `wallet_owner` e `wallet_app` e o banco `wallet` |
| `keycloak` | Keycloak 26 com o realm `wallet` importado de `deploy/keycloak/wallet-realm.json` |
| `localstack` | SQS, SNS e IAM locais |
| `aws-init` | Executa `deploy/aws/init-aws.sh`: filas, DLQ, redrive, tópico, fila de auditoria, políticas e usuários IAM com chaves de acesso |
| `migrate` | Aplica as migrations com o papel dono do schema (`wallet migrate up`) e termina |
| `app-1`, `app-2`, `app-3` | Três instâncias independentes com todos os papéis habilitados |

| Instância | API | Métricas |
| --- | --- | --- |
| `app-1` | http://localhost:8081 | http://localhost:9091/metrics |
| `app-2` | http://localhost:8082 | http://localhost:9092/metrics |
| `app-3` | http://localhost:8083 | http://localhost:9093/metrics |

Cada instância tem healthcheck (`/wallet health`) e `stop_grace_period` de 40 s, maior que o
`SHUTDOWN_TIMEOUT` padrão de 30 s.

Para derrubar tudo e apagar o volume do banco:

```sh
make down        # docker compose down -v
```

## Comandos da aplicação

O binário `wallet` (`cmd/wallet`) tem os subcomandos:

```text
wallet serve                executa os papéis listados em APP_ROLES
wallet migrate up           aplica todas as migrations pendentes
wallet migrate down <n|all> reverte as últimas n migrations, ou todas
wallet migrate version      mostra a versão atual do schema
wallet health               sai com 0 quando a instância local está pronta
```

`wallet health` consulta `GET /health/ready` no endereço de `HTTP_ADDR` (padrão `:8080`, sondado
em `127.0.0.1`). Ele existe porque a imagem final é distroless, sem shell nem `curl`.

## Variáveis de ambiente

Os valores locais estão em [`.env.example`](.env.example). Nenhum deles é segredo real.
A configuração é validada inteira na partida; todos os erros são reportados de uma vez e o
processo sai com código 2.

| Variável | Padrão | Descrição |
| --- | --- | --- |
| `APP_ROLES` | `api,consumer,outbox,pending` | Papéis executados pelo processo |
| `INSTANCE_ID` | hostname | Identifica a instância nos logs e nos leases da outbox |
| `HTTP_ADDR` | `:8080` | Endereço da API |
| `METRICS_ADDR` | `:9090` | Endereço de `/metrics` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` ou `error` |
| `SHUTDOWN_TIMEOUT` | `30s` | Prazo total do encerramento |
| `DATABASE_URL` | obrigatória | Conexão da aplicação (papel `wallet_app`) |
| `MIGRATIONS_DATABASE_URL` | — | Conexão usada por `wallet migrate` (papel `wallet_owner`); se ausente, usa `DATABASE_URL` |
| `DB_MAX_CONNS` | `20` | Tamanho máximo do pool |
| `DB_LOCK_TIMEOUT` | `2s` | `lock_timeout` de cada transação |
| `DB_STATEMENT_TIMEOUT` | `5s` | `statement_timeout` de cada transação |
| `DB_STARTUP_TIMEOUT` | `30s` | Tempo de espera pelo PostgreSQL na partida |
| `OIDC_ISSUER` | obrigatória com `api` | Issuer esperado nos tokens |
| `OIDC_JWKS_URL` | obrigatória com `api` | Endpoint JWKS (pode usar um hostname interno) |
| `OIDC_AUDIENCE` | obrigatória com `api` | Audience exigida (`wallet-api`) |
| `AWS_REGION` | `us-east-1` | Região AWS |
| `AWS_ENDPOINT_URL` | — | Endpoint alternativo (LocalStack) |
| `AWS_SHARED_CREDENTIALS_FILE`, `AWS_PROFILE` | `/aws/credentials`, `wallet-consumer` no compose | Arquivo e perfil de credenciais gerados pelo `aws-init` |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | — | Alternativa às credenciais do arquivo, fora do compose |
| `SQS_WAGER_QUEUE_URL` | obrigatória com `consumer` | Fila `wager-transactions.fifo` |
| `SQS_WAGER_DLQ_URL` | obrigatória com `consumer` | Fila `wager-transactions-dlq.fifo` |
| `SQS_ALLOWED_PROVIDERS` | obrigatória com `consumer` | Provedores aceitos na fila, separados por vírgula |
| `SQS_MAX_IN_FLIGHT` | `10` | Grupos de mensagens processados em paralelo |
| `SQS_MESSAGE_TIMEOUT` | `30s` | Prazo de tratamento de uma mensagem (menor que o visibility timeout de 60 s) |
| `SNS_EVENTS_TOPIC_ARN` | obrigatória com `outbox` | Tópico `wallet-events.fifo` |
| `OUTBOX_BATCH_SIZE` | `50` | Eventos por lote do relay |
| `OUTBOX_LEASE` | `30s` | Duração do lease de um evento reivindicado |
| `OUTBOX_POLL_INTERVAL` | `250ms` | Intervalo entre varreduras da outbox |
| `OUTBOX_BACKOFF_BASE` | `1s` | Atraso inicial após uma falha de publicação |
| `OUTBOX_BACKOFF_MAX` | `5m` | Atraso máximo entre tentativas de publicação |
| `OUTBOX_RETENTION` | `0` | Idade a partir da qual eventos publicados são apagados (mínimo `1h`); `0` mantém todos |
| `PENDING_POLL_INTERVAL` | `500ms` | Intervalo entre varreduras de pendências |
| `PENDING_LEASE` | `30s` | Lease de uma pendência reivindicada |
| `PENDING_BATCH_SIZE` | `50` | Pendências por lote |
| `PENDING_BACKOFF_BASE` | `2s` | Atraso inicial entre tentativas de resolver a referência |
| `PENDING_BACKOFF_MAX` | `5m` | Atraso máximo entre tentativas |
| `PENDING_MAX_ATTEMPTS` | `10` | Tentativas antes de rejeitar com `REFERENCE_NOT_FOUND` |
| `PENDING_REFERENCE_TTL` | `30m` | Prazo máximo de espera pela referência |

## Filas, tópico e IdP

O provisionamento é automático (`aws-init` e o import do realm). Para reexecutar o script de
AWS, que é idempotente:

```sh
docker compose up aws-init
```

Recursos criados pelo `deploy/aws/init-aws.sh`:

| Recurso | Configuração |
| --- | --- |
| `wager-transactions.fifo` | FIFO, sem deduplicação por conteúdo, visibility timeout 60 s, long polling de 20 s, retenção de 4 dias, redrive para a DLQ com `maxReceiveCount` 5 |
| `wager-transactions-dlq.fifo` | FIFO, retenção de 14 dias |
| `wallet-events.fifo` | Tópico SNS FIFO dos eventos de saída |
| `wallet-events-audit.fifo` | Fila assinante do tópico (raw delivery), útil para inspecionar eventos |
| Políticas IAM | `wallet-consumer` (consumir a fila, enviar à DLQ, publicar no tópico) e `provider-producer` (enviar à fila) |
| Usuários IAM | `wallet-consumer`, `provider-a-producer` e `provider-b-producer`, cada um com a política correspondente e uma chave de acesso nova a cada execução, gravada como perfil no volume `aws-credentials` (`/aws/credentials` nos containers da aplicação) |

As instâncias `app-1` a `app-3` usam o perfil `wallet-consumer`. A LocalStack Community não
avalia políticas IAM, então localmente as chaves identificam o chamador mas não restringem o
acesso; numa conta AWS as mesmas políticas passam a ser aplicadas.

Clientes do realm `wallet` (todos `client_credentials`, segredos apenas locais):

| Cliente | Segredo | Scopes | `provider_id` |
| --- | --- | --- | --- |
| `provider-a` | `provider-a-local-secret` | `wagering:write`, `wagering:read` | `provider-a` |
| `provider-b` | `provider-b-local-secret` | `wagering:write`, `wagering:read` | `provider-b` |
| `provider-a-short-lived` | `provider-a-short-lived-local-secret` | `wagering:write`, `wagering:read` | `provider-a` |
| `wallet-backoffice` | `wallet-backoffice-local-secret` | `wallets:read`, `wallets:write`, `wallets:reconcile`, `wagering:read-all` | — |
| `unrelated-service` | `unrelated-service-local-secret` | `wagering:write`, `wagering:read` (sem audience `wallet-api`) | — |

O console do Keycloak fica em http://localhost:8080 (usuário `admin`, senha `admin`).

## Migrations

As migrations ficam em `migrations/` e são embutidas no binário. Elas rodam com o papel
`wallet_owner`; a aplicação usa `wallet_app`, que não é dono das tabelas e por isso não consegue
desabilitar triggers nem alterar o schema.

Com o compose:

```sh
docker compose run --rm migrate migrate up
docker compose run --rm migrate migrate version
docker compose run --rm migrate migrate down 1     # ou "all"
```

A partir do host:

```sh
export MIGRATIONS_DATABASE_URL='postgres://wallet_owner:wallet_owner@localhost:5432/wallet?sslmode=disable'
make migrate-up
make migrate-version
make migrate-down        # reverte uma migration
```

## Exemplos de chamadas

Obter tokens:

```sh
token() {
  curl -s -X POST http://localhost:8080/realms/wallet/protocol/openid-connect/token \
    -d grant_type=client_credentials -d client_id="$1" -d client_secret="$1-local-secret" \
    | jq -r .access_token
}
BACKOFFICE=$(token wallet-backoffice)
PROVIDER_A=$(token provider-a)
API=http://localhost:8081
```

Abrir uma carteira (`wallets:write`):

```sh
curl -s -X POST $API/wallets \
  -H "Authorization: Bearer $BACKOFFICE" -H 'Content-Type: application/json' \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
       "initialBalance":{"amount":"1000.00","currency":"BRL"}}'
```

Resposta `201 Created` com `Location: /wallets/{id}`:

```json
{"id":"...","playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","balance":{"amount":"1000.00","currency":"BRL"},"version":1,"createdAt":"...","updatedAt":"..."}
```

Enviar uma aposta (`wagering:write`; o `providerId` do corpo precisa ser o do token):

```sh
WALLET=<id da carteira>
curl -s -X POST $API/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-123' \
  -d '{"providerId":"provider-a","externalTransactionId":"transaction-123",
       "playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"'$WALLET'",
       "roundId":"round-987","gameId":"fortune-chimp","kind":"BET",
       "money":{"amount":"25.00","currency":"BRL"}}'
```

```json
{"transactionId":"...","status":"PROCESSED","balance":{"amount":"975.00","currency":"BRL"},"idempotentReplay":false}
```

Repetir a mesma chamada devolve o mesmo resultado com `"idempotentReplay": true`. Para `REFUND`
e `ROLLBACK`, inclua `referenceExternalTransactionId`.

Consultas:

```sh
curl -s $API/wallets/$WALLET -H "Authorization: Bearer $BACKOFFICE"
curl -s "$API/wallets/$WALLET/ledger?limit=50" -H "Authorization: Bearer $BACKOFFICE"
curl -s $API/wagering/transactions/<transactionId> -H "Authorization: Bearer $PROVIDER_A"
curl -s $API/providers/provider-a/wagering/transactions/transaction-123 -H "Authorization: Bearer $PROVIDER_A"
curl -s -X POST $API/wallets/$WALLET/reconciliation -H "Authorization: Bearer $BACKOFFICE"
```

Enviar a mesma operação pela fila (o consumidor usa `data.idempotencyKey` e o `messageId` do
envelope; o `MessageGroupId` usado nos testes é o `walletId`):

```sh
cat > /tmp/msg.json <<EOF
{"messageId":"msg-123","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",
 "data":{"providerId":"provider-a","externalTransactionId":"transaction-124",
         "idempotencyKey":"provider-a:transaction-124",
         "playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"$WALLET",
         "roundId":"round-987","gameId":"fortune-chimp","kind":"BET",
         "money":{"amount":"10.00","currency":"BRL"}}}
EOF
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1 \
aws --endpoint-url http://localhost:4566 sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id "$WALLET" --message-deduplication-id msg-123 \
  --message-body file:///tmp/msg.json
```

Ler os eventos publicados:

```sh
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1 \
aws --endpoint-url http://localhost:4566 sqs receive-message --max-number-of-messages 10 \
  --queue-url http://localhost:4566/000000000000/wallet-events-audit.fifo
```

Health e métricas (sem autenticação):

```sh
curl -s $API/health/live
curl -s $API/health/ready
curl -s http://localhost:9091/metrics | grep -E '^(wager_|wallet_|sqs_|outbox_|pending_|inbox_|db_)'
```

Os endpoints, scopes e códigos de resposta estão documentados em
[ARCHITECTURE.md](ARCHITECTURE.md#contrato-http).

## Testes

| Comando | O que roda | Dependências |
| --- | --- | --- |
| `make test` | `go test ./...`: unitários de domínio, aplicação, adaptadores, configuração e grafo Fx | nenhuma |
| `make test-race` | `go test -race ./...` | nenhuma |
| `make test-integration` | `go test -race -tags=integration -count=1 ./test/integration/...` | PostgreSQL, Keycloak e LocalStack |
| `make test-e2e` | `go test -race -tags=e2e -count=1 -timeout=20m ./test/e2e/...` | PostgreSQL, Keycloak e LocalStack |
| `make vet` | `go vet` com e sem as tags `integration,e2e` | nenhuma |
| `make fmt-check` | Falha se algum arquivo não estiver formatado com `gofmt -s` | nenhuma |
| `make lint` | `golangci-lint run ./...` com a versão v2.5.0 fixada, via `go run` (sobrescreva com `GOLANGCI_LINT=golangci-lint`) | nenhuma |

### Preparar as dependências

```sh
make deps-up      # postgres, keycloak e localstack saudáveis, depois aws-init e migrate
```

Se as instâncias `app-1`..`app-3` estiverem rodando, pare-as antes dos testes, porque elas
consomem a mesma fila e ocupam as portas:

```sh
docker compose stop app-1 app-2 app-3
```

Os testes usam, por padrão, os endereços do compose no host. Para outro ambiente:

| Variável | Padrão |
| --- | --- |
| `TEST_DATABASE_OWNER_URL` | `postgres://wallet_owner:wallet_owner@localhost:5432/wallet?sslmode=disable` |
| `TEST_DATABASE_APP_URL` | `postgres://wallet_app:wallet_app@localhost:5432/wallet?sslmode=disable` |
| `TEST_AWS_ENDPOINT_URL` | `http://localhost:4566` |
| `TEST_KEYCLOAK_URL` | `http://localhost:8080` |

### Integração (`-tags=integration`)

Rodam contra PostgreSQL, Keycloak e LocalStack reais. Cada teste recebe um banco próprio,
clonado de um template migrado. Cobrem migrations (subida e reversão), constraints e triggers de
proteção do ledger, repositórios, casos de uso (abertura, cinco tipos de operação, reversões,
pendências, consultas), concorrência na mesma carteira (duas apostas de 80.00 sobre 100.00:
uma processada, uma `INSUFFICIENT_FUNDS`, saldo 20.00 e um único débito no ledger), inbox, outbox concorrente, API HTTP,
SQS/SNS com DLQ, readiness e o ciclo de vida Fx.

A autenticação é coberta de duas formas. `TestKeycloakCredentials` obtém tokens reais do
Keycloak por client credentials e verifica credenciais ausentes, com assinatura adulterada,
malformadas, expiradas (cliente `provider-a-short-lived`, tokens de 2 s) e de outra audiência
(`unrelated-service`), além de escopo insuficiente e isolamento entre provedores. Os demais
testes HTTP usam um emissor em processo (`internal/adapter/auth/authtest`) para casos que o
Keycloak não produz sob demanda: chave desconhecida, issuer errado e claims arbitrários.

### Múltiplos processos e falhas (`-tags=e2e`)

O `TestMain` compila `cmd/wallet` com a build tag `faultinject` e os testes sobem várias
instâncias do binário como processos independentes, cada uma com banco, filas e tópico
próprios do teste. Cenários:

- mesma aposta enviada 50 vezes em paralelo para três instâncias gera um único débito;
- apostas de 80.00, 80.00 e 100.00 disputando um saldo de 100.00 em três instâncias: exatamente
  uma processada, as outras rejeitadas com `422`, saldo e ledger consistentes;
- carteiras distintas progredindo em paralelo;
- consumidor morto depois do commit e antes de remover a mensagem: reentrega sem efeito duplo;
- relay morto depois de publicar e antes de marcar: outra instância republica com o mesmo `eventId`;
- worker de pendências morto depois de reivindicar: retomada após o lease;
- parada graciosa sob carga e morte de todas as instâncias seguida de reinício;
- mesma operação por HTTP e SQS com um único efeito;
- `REFUND` antes da `BET` resolvido quando a aposta chega, e rejeitado com
  `REFERENCE_NOT_FOUND` quando ela não chega dentro do `PENDING_REFERENCE_TTL`.

A injeção de falhas usa a variável `FAULT_POINT`, lida apenas em binários compilados com
`-tags faultinject`. Os pontos são `consumer.after_commit`, `outbox.after_publish` e
`pending.after_claim`; ao atingi-los o processo envia `SIGKILL` a si mesmo. Sem a tag, as
chamadas são no-ops e não existem no binário de produção.

## Estrutura do repositório

```text
cmd/wallet/                 binário: serve, migrate, health
internal/domain/            money, wallet, wagering, event (sem dependências de infraestrutura)
internal/app/               casos de uso e portas
internal/adapter/httpapi/   API HTTP, problem details, health
internal/adapter/auth/      verificação de tokens OIDC
internal/adapter/postgres/  unit of work, repositórios, outbox, pendências, migrator
internal/adapter/sqsconsumer/  consumidor SQS FIFO e DLQ
internal/adapter/snspublisher/ publicação no SNS FIFO
internal/platform/          logging, métricas, lifecycle de workers, injeção de falhas
internal/config/            carga e validação do ambiente
internal/wiring/            módulos Fx por papel
migrations/                 schema versionado
deploy/                     init do PostgreSQL, realm do Keycloak, provisionamento AWS
test/integration/, test/e2e/
```
