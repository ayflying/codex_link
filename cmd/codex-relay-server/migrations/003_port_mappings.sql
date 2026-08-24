CREATE TABLE IF NOT EXISTS port_mappings (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  device_id VARCHAR(128) NOT NULL,
  name VARCHAR(120) NOT NULL,
  target_host VARCHAR(255) NOT NULL,
  target_port INT NOT NULL,
  listen_port INT NOT NULL,
  protocol VARCHAR(16) NOT NULL DEFAULT 'tcp',
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at VARCHAR(64) NOT NULL,
  updated_at VARCHAR(64) NOT NULL,
  CONSTRAINT fk_port_mappings_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT fk_port_mappings_device FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
  UNIQUE KEY uq_port_mappings_listen_port (listen_port),
  INDEX idx_port_mappings_user (user_id),
  INDEX idx_port_mappings_device (device_id),
  INDEX idx_port_mappings_enabled (enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
