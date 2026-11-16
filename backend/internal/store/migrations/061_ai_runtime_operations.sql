ALTER TABLE plume.ai_security_runtime_sources
    ADD COLUMN token_version bigint NOT NULL DEFAULT 1 CHECK (token_version > 0);
