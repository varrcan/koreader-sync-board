-- Последнее событие устройства: что сообщила о себе читалка (экран устройств)
-- и лента активности по устройству.
CREATE INDEX progress_events_credential ON progress_events (credential_id, id);
