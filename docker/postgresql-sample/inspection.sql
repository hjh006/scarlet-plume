ALTER TABLE inventory.customers
    ADD COLUMN IF NOT EXISTS customer_email text,
    ADD COLUMN IF NOT EXISTS mobile_phone text,
    ADD COLUMN IF NOT EXISTS national_id text;

DO $sample$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sample_analyst') THEN
        CREATE ROLE sample_analyst NOLOGIN;
    END IF;
END
$sample$;

GRANT USAGE ON SCHEMA inventory TO sample_analyst;
GRANT SELECT ON TABLE inventory.orders TO sample_analyst;
GRANT SELECT (customer_email) ON TABLE inventory.customers TO sample_analyst;
