CREATE TABLE IF NOT EXISTS apps (
 id bigserial PRIMARY KEY, name text NOT NULL CHECK(length(trim(name))>0), package_name text NOT NULL DEFAULT '',
 description text NOT NULL DEFAULT '', is_active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS test_cases (
 id bigserial PRIMARY KEY, app_id bigint NOT NULL REFERENCES apps(id), case_key text NOT NULL CHECK(length(trim(case_key))>0),
 category text NOT NULL DEFAULT '', title text NOT NULL CHECK(length(trim(title))>0), precondition text NOT NULL DEFAULT '',
 steps text NOT NULL, expected_result text NOT NULL, test_type text NOT NULL CHECK(test_type IN ('manual','automated','both')),
 sort_order integer NOT NULL DEFAULT 0, is_active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(app_id,case_key)
);
CREATE TABLE IF NOT EXISTS test_runs (
 id bigserial PRIMARY KEY, app_id bigint NOT NULL REFERENCES apps(id), run_type text NOT NULL CHECK(run_type IN ('manual','automated')),
 runner_name text NOT NULL DEFAULT '', executed_by text NOT NULL DEFAULT '', started_at timestamptz NOT NULL,
 finished_at timestamptz, app_version text NOT NULL DEFAULT '', device_name text NOT NULL DEFAULT '', android_version text NOT NULL DEFAULT '',
 notes text NOT NULL DEFAULT '', summary text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(finished_at IS NULL OR finished_at >= started_at)
);
CREATE TABLE IF NOT EXISTS test_results (
 id bigserial PRIMARY KEY, test_run_id bigint NOT NULL REFERENCES test_runs(id), test_case_id bigint NOT NULL REFERENCES test_cases(id),
 status text NOT NULL DEFAULT 'not_run' CHECK(status IN ('not_run','passed','failed','blocked')),
 actual_result text NOT NULL DEFAULT '', comment text NOT NULL DEFAULT '', case_snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(test_run_id,test_case_id)
);
CREATE TABLE IF NOT EXISTS attachments (
 id bigserial PRIMARY KEY, test_result_id bigint NOT NULL REFERENCES test_results(id), file_name text NOT NULL,
 storage_path text NOT NULL UNIQUE, mime_type text NOT NULL, file_size_bytes bigint NOT NULL CHECK(file_size_bytes>0),
 uploaded_at timestamptz NOT NULL DEFAULT now(), uploaded_by text NOT NULL DEFAULT '', caption text NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS edit_history (
 id bigserial PRIMARY KEY, test_case_id bigint REFERENCES test_cases(id), test_result_id bigint REFERENCES test_results(id),
 action text NOT NULL CHECK(action IN ('created','updated','deactivated','restored')), before_data jsonb, after_data jsonb NOT NULL,
 changed_at timestamptz NOT NULL DEFAULT now(), changed_by text NOT NULL DEFAULT '', reason text NOT NULL DEFAULT '',
 CHECK((test_case_id IS NULL) <> (test_result_id IS NULL))
);
CREATE INDEX IF NOT EXISTS cases_app_idx ON test_cases(app_id,sort_order,id);
CREATE INDEX IF NOT EXISTS runs_app_idx ON test_runs(app_id,started_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS results_case_idx ON test_results(test_case_id,test_run_id);
CREATE INDEX IF NOT EXISTS history_case_idx ON edit_history(test_case_id,id DESC);
CREATE INDEX IF NOT EXISTS history_result_idx ON edit_history(test_result_id,id DESC);
