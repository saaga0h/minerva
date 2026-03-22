# Vault policy for Minerva Nomad jobs.
# Apply with: vault policy write minerva deploy/vault/minerva-policy.hcl
#
# Note: KV v2 stores data under secret/data/<path> even though
# `vault kv put` uses secret/<path> on the CLI.

path "secret/data/nomad/minerva" {
  capabilities = ["read"]
}
