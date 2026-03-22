# minerva-daemons.hcl
# Long-running Minerva pipeline primitives.
#
# Prerequisites:
#   - Vault policy "minerva" applied (deploy/vault/minerva-policy.hcl)
#   - raw_exec enabled on Nomad client:
#       plugin "raw_exec" { config { enabled = true } }
#   - enable_script_checks = true on Nomad client (for Consul health checks)
#   - Consul agent running on Nomad client
#
# Deploy:
#   nomad job run deploy/nomad/minerva-daemons.hcl
#
# Startup order matters: state and store must connect to Mosquitto before
# the trigger fires. Nomad starts all tasks in the group concurrently —
# primitives handle reconnection internally via paho.

job "minerva-daemons" {
  datacenters = ["the-collective"]
  type        = "service"

  constraint {
    attribute = "${meta.gpu}"
    operator  = "!="
    value     = "true"
  }

  group "daemons" {
    count = 1

    restart {
      attempts = 5
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    # ── source-freshrss ───────────────────────────────────────────────────────

    task "source-freshrss" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/source-freshrss && exec ${NOMAD_TASK_DIR}/source-freshrss"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/source-freshrss"
        destination = "local/source-freshrss"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
DB_HOST={{ .Data.data.DB_HOST }}
DB_PORT={{ .Data.data.DB_PORT }}
DB_USER={{ .Data.data.DB_USER }}
DB_PASSWORD={{ .Data.data.DB_PASSWORD }}
DB_NAME={{ .Data.data.DB_NAME }}
DB_SSLMODE={{ .Data.data.DB_SSLMODE }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
FRESHRSS_BASE_URL={{ .Data.data.FRESHRSS_BASE_URL }}
FRESHRSS_API_KEY={{ .Data.data.FRESHRSS_API_KEY }}
FRESHRSS_TIMEOUT={{ .Data.data.FRESHRSS_TIMEOUT }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 100
        memory = 64
      }
      service {
        name = "minerva-source-freshrss"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x source-freshrss > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── source-miniflux ───────────────────────────────────────────────────────

    task "source-miniflux" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/source-miniflux && exec ${NOMAD_TASK_DIR}/source-miniflux"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/source-miniflux"
        destination = "local/source-miniflux"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
DB_HOST={{ .Data.data.DB_HOST }}
DB_PORT={{ .Data.data.DB_PORT }}
DB_USER={{ .Data.data.DB_USER }}
DB_PASSWORD={{ .Data.data.DB_PASSWORD }}
DB_NAME={{ .Data.data.DB_NAME }}
DB_SSLMODE={{ .Data.data.DB_SSLMODE }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
MINIFLUX_BASE_URL={{ .Data.data.MINIFLUX_BASE_URL }}
MINIFLUX_API_KEY={{ .Data.data.MINIFLUX_API_KEY }}
MINIFLUX_TIMEOUT={{ .Data.data.MINIFLUX_TIMEOUT }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 100
        memory = 64
      }
      service {
        name = "minerva-source-miniflux"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x source-miniflux > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── source-linkwarden ─────────────────────────────────────────────────────

    task "source-linkwarden" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/source-linkwarden && exec ${NOMAD_TASK_DIR}/source-linkwarden"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/source-linkwarden"
        destination = "local/source-linkwarden"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
DB_HOST={{ .Data.data.DB_HOST }}
DB_PORT={{ .Data.data.DB_PORT }}
DB_USER={{ .Data.data.DB_USER }}
DB_PASSWORD={{ .Data.data.DB_PASSWORD }}
DB_NAME={{ .Data.data.DB_NAME }}
DB_SSLMODE={{ .Data.data.DB_SSLMODE }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
LINKWARDEN_BASE_URL={{ .Data.data.LINKWARDEN_BASE_URL }}
LINKWARDEN_API_KEY={{ .Data.data.LINKWARDEN_API_KEY }}
LINKWARDEN_TIMEOUT={{ .Data.data.LINKWARDEN_TIMEOUT }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 100
        memory = 64
      }
      service {
        name = "minerva-source-linkwarden"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x source-linkwarden > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── extractor ─────────────────────────────────────────────────────────────

    task "extractor" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/extractor && exec ${NOMAD_TASK_DIR}/extractor"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/extractor"
        destination = "local/extractor"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
EXTRACTOR_USER_AGENT={{ .Data.data.EXTRACTOR_USER_AGENT }}
EXTRACTOR_TIMEOUT={{ .Data.data.EXTRACTOR_TIMEOUT }}
EXTRACTOR_MAX_SIZE={{ .Data.data.EXTRACTOR_MAX_SIZE }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 200
        memory = 128
      }
      service {
        name = "minerva-extractor"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x extractor > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── analyzer ──────────────────────────────────────────────────────────────

    task "analyzer" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/analyzer && exec ${NOMAD_TASK_DIR}/analyzer"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/analyzer"
        destination = "local/analyzer"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
OLLAMA_BASE_URL={{ .Data.data.OLLAMA_BASE_URL }}
OLLAMA_MODEL={{ .Data.data.OLLAMA_MODEL }}
OLLAMA_TIMEOUT={{ .Data.data.OLLAMA_TIMEOUT }}
OLLAMA_MAX_TOKENS={{ .Data.data.OLLAMA_MAX_TOKENS }}
OLLAMA_TEMPERATURE={{ .Data.data.OLLAMA_TEMPERATURE }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      # Analyzer is CPU-heavy during Ollama inference; memory for prompt/response buffers
      resources {
        cpu    = 500
        memory = 256
      }
      service {
        name = "minerva-analyzer"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x analyzer > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── search-openlibrary ────────────────────────────────────────────────────

    task "search-openlibrary" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/search-openlibrary && exec ${NOMAD_TASK_DIR}/search-openlibrary"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/search-openlibrary"
        destination = "local/search-openlibrary"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
OPENLIBRARY_TIMEOUT={{ .Data.data.OPENLIBRARY_TIMEOUT }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 100
        memory = 64
      }
      service {
        name = "minerva-search-openlibrary"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x search-openlibrary > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── search-arxiv ──────────────────────────────────────────────────────────

    task "search-arxiv" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/search-arxiv && exec ${NOMAD_TASK_DIR}/search-arxiv"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/search-arxiv"
        destination = "local/search-arxiv"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
ARXIV_TIMEOUT={{ .Data.data.ARXIV_TIMEOUT }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 100
        memory = 64
      }
      service {
        name = "minerva-search-arxiv"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x search-arxiv > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── search-semantic-scholar ───────────────────────────────────────────────

    task "search-semantic-scholar" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/search-semantic-scholar && exec ${NOMAD_TASK_DIR}/search-semantic-scholar"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/search-semantic-scholar"
        destination = "local/search-semantic-scholar"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
SEMANTIC_SCHOLAR_TIMEOUT={{ .Data.data.SEMANTIC_SCHOLAR_TIMEOUT }}
SEMANTIC_SCHOLAR_API_KEY={{ .Data.data.SEMANTIC_SCHOLAR_API_KEY }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 100
        memory = 64
      }
      service {
        name = "minerva-search-semantic-scholar"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x search-semantic-scholar > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── koha-check ────────────────────────────────────────────────────────────

    task "koha-check" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/koha-check && exec ${NOMAD_TASK_DIR}/koha-check"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/koha-check"
        destination = "local/koha-check"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
KOHA_BASE_URL={{ .Data.data.KOHA_BASE_URL }}
KOHA_USERNAME={{ .Data.data.KOHA_USERNAME }}
KOHA_PASSWORD={{ .Data.data.KOHA_PASSWORD }}
KOHA_TIMEOUT={{ .Data.data.KOHA_TIMEOUT }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 100
        memory = 64
      }
      service {
        name = "minerva-koha-check"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x koha-check > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── notifier ──────────────────────────────────────────────────────────────

    task "notifier" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/notifier && exec ${NOMAD_TASK_DIR}/notifier"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/notifier"
        destination = "local/notifier"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
NTFY_BASE_URL={{ .Data.data.NTFY_BASE_URL }}
NTFY_TOPIC={{ .Data.data.NTFY_TOPIC }}
NTFY_TOKEN={{ .Data.data.NTFY_TOKEN }}
NTFY_PRIORITY={{ .Data.data.NTFY_PRIORITY }}
NTFY_ENABLED={{ .Data.data.NTFY_ENABLED }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 100
        memory = 64
      }
      service {
        name = "minerva-notifier"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x notifier > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── store ─────────────────────────────────────────────────────────────────

    task "store" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/store && exec ${NOMAD_TASK_DIR}/store"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/store"
        destination = "local/store"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
DB_HOST={{ .Data.data.DB_HOST }}
DB_PORT={{ .Data.data.DB_PORT }}
DB_USER={{ .Data.data.DB_USER }}
DB_PASSWORD={{ .Data.data.DB_PASSWORD }}
DB_NAME={{ .Data.data.DB_NAME }}
DB_SSLMODE={{ .Data.data.DB_SSLMODE }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 100
        memory = 128
      }
      service {
        name = "minerva-store"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x store > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

    # ── state ─────────────────────────────────────────────────────────────────

    task "state" {
      driver = "raw_exec"
      config {
        command = "/bin/sh"
        args    = ["-c", "chmod +x ${NOMAD_TASK_DIR}/state && exec ${NOMAD_TASK_DIR}/state"]
      }
      artifact {
        source      = "http://192.168.10.50:8080/api/binaries/minerva/${attr.cpu.arch}/state"
        destination = "local/state"
        mode        = "file"
      }
      template {
        destination = "secrets/minerva.env"
        env         = true
        data        = <<EOT
{{ with secret "secret/data/nomad/minerva" }}
DB_HOST={{ .Data.data.DB_HOST }}
DB_PORT={{ .Data.data.DB_PORT }}
DB_USER={{ .Data.data.DB_USER }}
DB_PASSWORD={{ .Data.data.DB_PASSWORD }}
DB_NAME={{ .Data.data.DB_NAME }}
DB_SSLMODE={{ .Data.data.DB_SSLMODE }}
MQTT_BROKER_URL={{ .Data.data.MQTT_BROKER_URL }}
LOG_LEVEL={{ .Data.data.LOG_LEVEL }}
{{ end }}
EOT
      }
      vault {
        policies = ["minerva"]
      }
      resources {
        cpu    = 100
        memory = 64
      }
      service {
        name = "minerva-state"
        tags = ["minerva"]
        check {
          type     = "script"
          name     = "process-alive"
          command  = "/bin/sh"
          args     = ["-c", "pgrep -x state > /dev/null"]
          interval = "30s"
          timeout  = "5s"
        }
      }
    }

  }
}
