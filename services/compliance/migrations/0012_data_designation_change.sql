ALTER TABLE data_designation DROP CONSTRAINT IF EXISTS data_designation_class_check;
ALTER TABLE data_designation ADD CONSTRAINT data_designation_class_check CHECK (class IN ('CUI', 'NONE'));
