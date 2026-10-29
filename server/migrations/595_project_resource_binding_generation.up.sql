ALTER TABLE project_resource ADD COLUMN IF NOT EXISTS binding_generation bigint NOT NULL DEFAULT 1;

CREATE OR REPLACE FUNCTION project_resource_binding_generation_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_mode jsonb;
    new_mode jsonb;
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.binding_generation := 1;
        RETURN NEW;
    END IF;
    old_mode := CASE WHEN OLD.resource_ref->>'execution_mode' IS NULL OR OLD.resource_ref->>'execution_mode' = '' THEN '"in_place"'::jsonb ELSE OLD.resource_ref->'execution_mode' END;
    new_mode := CASE WHEN NEW.resource_ref->>'execution_mode' IS NULL OR NEW.resource_ref->>'execution_mode' = '' THEN '"in_place"'::jsonb ELSE NEW.resource_ref->'execution_mode' END;
    IF NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.resource_type IS DISTINCT FROM OLD.resource_type
       OR NEW.resource_ref->'local_path' IS DISTINCT FROM OLD.resource_ref->'local_path'
       OR NEW.resource_ref->'daemon_id' IS DISTINCT FROM OLD.resource_ref->'daemon_id'
       OR new_mode IS DISTINCT FROM old_mode THEN
        IF OLD.binding_generation <= 0 OR OLD.binding_generation = 9223372036854775807 THEN
            RAISE EXCEPTION 'resource binding generation unavailable';
        END IF;
        NEW.binding_generation := OLD.binding_generation + 1;
    ELSE
        -- Ignore caller-supplied generations, including old SQL clients.
        NEW.binding_generation := OLD.binding_generation;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS project_resource_binding_generation ON project_resource;
CREATE TRIGGER project_resource_binding_generation
BEFORE INSERT OR UPDATE ON project_resource
FOR EACH ROW EXECUTE FUNCTION project_resource_binding_generation_guard();
