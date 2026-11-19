alter table plan_version add column if not exists warnings jsonb not null default '[]'::jsonb;
