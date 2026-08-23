CREATE TABLE IF NOT EXISTS users (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  username VARCHAR(48) NOT NULL,
  username_key VARCHAR(48) NOT NULL UNIQUE,
  password_hash CHAR(64) NOT NULL,
  salt VARCHAR(128) NOT NULL,
  iterations INT NOT NULL,
  created_at VARCHAR(64) NOT NULL,
  updated_at VARCHAR(64) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS access_tokens (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  name VARCHAR(120) NOT NULL,
  token_value VARCHAR(255) NOT NULL,
  token_hash CHAR(64) NOT NULL UNIQUE,
  token_prefix VARCHAR(32) NOT NULL,
  created_at VARCHAR(64) NOT NULL,
  updated_at VARCHAR(64) NOT NULL,
  refreshed_at VARCHAR(64) NULL,
  last_used_at VARCHAR(64) NULL,
  last_used_device_id VARCHAR(128) NULL,
  CONSTRAINT fk_access_tokens_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  INDEX idx_access_tokens_user (user_id),
  INDEX idx_access_tokens_last_used (last_used_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS devices (
  id VARCHAR(128) NOT NULL PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  token_id VARCHAR(64) NULL,
  name VARCHAR(160) NOT NULL,
  token_prefix VARCHAR(32) NULL,
  created_at VARCHAR(64) NOT NULL,
  updated_at VARCHAR(64) NOT NULL,
  last_seen_at VARCHAR(64) NULL,
  CONSTRAINT fk_devices_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT fk_devices_token FOREIGN KEY (token_id) REFERENCES access_tokens(id) ON DELETE SET NULL,
  INDEX idx_devices_user (user_id),
  INDEX idx_devices_token (token_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS web_sessions (
  token_hash CHAR(64) NOT NULL PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  CONSTRAINT fk_web_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  INDEX idx_web_sessions_expiry (expires_at),
  INDEX idx_web_sessions_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS sessions (
  user_id VARCHAR(64) NOT NULL,
  id VARCHAR(128) NOT NULL,
  device_id VARCHAR(128) NOT NULL,
  title TEXT NOT NULL,
  mode VARCHAR(64) NOT NULL,
  status VARCHAR(64) NOT NULL,
  created_at VARCHAR(64) NOT NULL,
  updated_at VARCHAR(64) NOT NULL,
  cwd TEXT NULL,
  note TEXT NULL,
  PRIMARY KEY (user_id, id),
  CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  INDEX idx_sessions_user_updated (user_id, updated_at),
  INDEX idx_sessions_device (user_id, device_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS events (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  device_id VARCHAR(128) NOT NULL,
  session_id VARCHAR(128) NOT NULL,
  event_type VARCHAR(64) NOT NULL,
  event_ts VARCHAR(64) NOT NULL,
  payload_json LONGTEXT NOT NULL,
  CONSTRAINT fk_events_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  INDEX idx_events_session (user_id, session_id, id),
  INDEX idx_events_device (user_id, device_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS uploads (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  mime_type VARCHAR(120) NOT NULL,
  file_path TEXT NOT NULL,
  file_size BIGINT NOT NULL,
  created_at VARCHAR(64) NOT NULL,
  CONSTRAINT fk_uploads_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  INDEX idx_uploads_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
