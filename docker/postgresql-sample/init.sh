#!/usr/bin/env bash
(
set -euo pipefail

psql -X --set ON_ERROR_STOP=1 --single-transaction --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --file - <<'SQL'
\getenv reader_password SCARLET_SAMPLE_READER_PASSWORD

SELECT format('CREATE ROLE sample_reader LOGIN PASSWORD %L', :'reader_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sample_reader')
\gexec
REVOKE CONNECT, TEMP ON DATABASE scarlet_sample FROM PUBLIC;
GRANT CONNECT ON DATABASE scarlet_sample TO sample_reader;
REVOKE ALL ON SCHEMA public FROM PUBLIC;

CREATE SCHEMA IF NOT EXISTS inventory;
CREATE TABLE IF NOT EXISTS inventory.customers (id bigint PRIMARY KEY, segment text NOT NULL);
CREATE TABLE IF NOT EXISTS inventory.orders (id bigint PRIMARY KEY, customer_id bigint NOT NULL REFERENCES inventory.customers(id));
CREATE TABLE IF NOT EXISTS inventory.shipments (id bigint PRIMARY KEY, order_id bigint NOT NULL REFERENCES inventory.orders(id));
INSERT INTO inventory.customers (id, segment) VALUES (1, 'synthetic'), (2, 'synthetic-validation') ON CONFLICT DO NOTHING;
INSERT INTO inventory.orders VALUES (1, 1) ON CONFLICT DO NOTHING;
INSERT INTO inventory.shipments VALUES (1, 1) ON CONFLICT DO NOTHING;

GRANT USAGE ON SCHEMA inventory TO sample_reader;
SQL
)
