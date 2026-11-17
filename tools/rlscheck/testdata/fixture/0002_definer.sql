-- SECURITY DEFINER functions: only a search_path that ends with pg_temp is safe (pg_temp is otherwise searched first
-- for relations, so a caller's temp table or view would run as the owner). search_path FROM CURRENT stores the
-- migration's path, which leaves pg_temp out.
create function definer_no_path() returns int language sql security definer as $$ select 1 $$;
create function definer_from_current() returns int language sql security definer set search_path from current as $$ select 1 $$;
create function definer_temp_first() returns int language sql security definer set search_path = pg_temp, pg_catalog as $$ select 1 $$;
create function definer_ok() returns int language sql security definer set search_path = pg_catalog, public, pg_temp as $$ select 1 $$;
create function invoker_no_path() returns int language sql as $$ select 1 $$;
