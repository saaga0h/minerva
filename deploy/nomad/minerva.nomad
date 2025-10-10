job "minerva" {
  datacenters = ["dc1"]
  type        = "batch"

  # Run nightly at 2 AM
  periodic {
    cron             = "0 2 * * *"
    prohibit_overlap = true
    time_zone        = "UTC"
  }

  group "minerva" {
    count = 1

    # Restart policy for batch jobs
    restart {
      attempts = 2
      interval = "30m"
      delay    = "15s"
      mode     = "fail"
    }

    # Resource allocation
    task "minerva" {
      driver = "docker"

      config {
        image = "minerva:latest"
        
        # Mount data volume for SQLite database persistence
        volumes = [
          "/opt/minerva/data:/data"
        ]

        # Logging configuration for vector/Loki integration
        logging {
          type = "json-file"
          config {
            max-size = "10m"
            max-file = "3"
          }
        }
      }

      # Environment variables
      env {
        # Application
        APP_NAME = "minerva"
        APP_ENV  = "production"
        
        # Logging - JSON format for structured logging
        LOG_LEVEL  = "info"
        LOG_FORMAT = "json"
        
        # Database
        DATABASE_PATH = "/data/minerva.db"
        
        # Service timeouts
        FRESHRSS_TIMEOUT  = "60"
        OLLAMA_TIMEOUT    = "600"
        SEARXNG_TIMEOUT   = "60"
        EXTRACTOR_TIMEOUT = "60"
      }

      # Template for sensitive configuration
      template {
        data = <<EOH
# FreshRSS Configuration
FRESHRSS_BASE_URL="{{ key "minerva/freshrss/base_url" }}"
FRESHRSS_USERNAME="{{ key "minerva/freshrss/username" }}"
FRESHRSS_PASSWORD="{{ key "minerva/freshrss/password" }}"

# Ollama Configuration
OLLAMA_BASE_URL="{{ key "minerva/ollama/base_url" }}"
OLLAMA_MODEL="{{ key "minerva/ollama/model" }}"

# SearXNG Configuration  
SEARXNG_BASE_URL="{{ key "minerva/searxng/base_url" }}"
EOH
        destination = "secrets/config.env"
        env         = true
      }

      # Resource requirements
      resources {
        cpu    = 500  # 500 MHz
        memory = 512  # 512 MB
      }

      # Health checking - not applicable for batch jobs but good for monitoring
      service {
        name = "minerva"
        tags = ["ai", "curator", "batch"]
        
        check {
          type     = "script"
          name     = "minerva-health"
          command  = "/bin/sh"
          args     = ["-c", "test -f /data/minerva.db"]
          interval = "30s"
          timeout  = "5s"
        }
      }

      # Logs configuration for vector collection
      logs {
        max_files     = 3
        max_file_size = 10
      }
    }
  }
}