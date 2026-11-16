ALTER TABLE plume.ai_gateway_credentials
    ADD COLUMN workload_binding_revision bigint CHECK(workload_binding_revision>0),
    ADD COLUMN revoked_at timestamptz,
    ADD COLUMN revocation_reason text CHECK(revocation_reason IN ('manual','binding_changed','binding_revoked')),
    ADD CONSTRAINT ai_gateway_credential_mint_binding CHECK(workload_binding_revision IS NULL OR workload_binding_id IS NOT NULL),
    ADD CONSTRAINT ai_gateway_credential_revocation_fact CHECK((revoked_at IS NULL AND revocation_reason IS NULL) OR (revoked_at IS NOT NULL AND revocation_reason IS NOT NULL AND NOT enabled));

-- Do not infer the issuing revision or a historical revocation cause for old
-- rows. Expiry maintenance and explicit revocation are different facts.
CREATE FUNCTION plume.ai_gateway_credential_provenance_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(OLD.workload_binding_id,OLD.workload_binding_revision) IS DISTINCT FROM ROW(NEW.workload_binding_id,NEW.workload_binding_revision) THEN
        RAISE EXCEPTION 'credential issuing binding and revision are immutable';
    END IF;
    IF OLD.revoked_at IS NOT NULL AND ROW(OLD.revoked_at,OLD.revocation_reason) IS DISTINCT FROM ROW(NEW.revoked_at,NEW.revocation_reason) THEN
        RAISE EXCEPTION 'recorded credential revocation facts are immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER ai_gateway_credential_provenance_no_rewrite BEFORE UPDATE ON plume.ai_gateway_credentials
FOR EACH ROW EXECUTE FUNCTION plume.ai_gateway_credential_provenance_guard();
