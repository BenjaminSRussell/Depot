-- Extensions stay in public: services/party migration 0001 qualifies public.gin_trgm_ops.
CREATE ROLE depot_app LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD 'depot_app';
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE DATABASE depot_restore;
