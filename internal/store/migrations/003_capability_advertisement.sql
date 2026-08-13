ALTER TABLE capabilities
ADD COLUMN advertisement_state TEXT NOT NULL DEFAULT 'unknown'
CHECK (advertisement_state IN ('unknown', 'fully_advertised', 'name_only', 'not_advertised'));
