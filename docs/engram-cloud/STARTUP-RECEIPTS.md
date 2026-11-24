# Prepare a Cloud startup receipt

An enrolled Engram Cloud operator can prepare a local receipt before restarting the server. The receipt records which PostgreSQL snapshot already had complete session indexing and mutation chunk coverage. Later starts try a bounded audit check and run ordinary startup repair if that check cannot prove coverage.

This opt-in expects a Linux Cloud process, PostgreSQL 16 or later, and a finite `ENGRAM_CLOUD_ALLOWED_PROJECTS` list. The PostgreSQL database and client connection must both use UTF-8 so the proof can bound the bytes returned by PostgreSQL. Use an existing private directory owned by the Cloud process user for `ENGRAM_CLOUD_STARTUP_RECEIPT_DIR`. The process needs to read and write the directory. Use storage local to the Cloud deployment with persistent space for the receipt. Each directory serves one configured PostgreSQL database and project list.

The `cloud_mutations`, `cloud_chunks` and `cloud_project_sessions` tables in `public` must be ordinary permanent tables without row security or inheritance. Enrollment rejects unsupported sources. After enrollment, startup uses ordinary repair whenever source eligibility cannot be verified or a replicated write needs verification through the full scan.

## Install and prepare

1. Identify an existing working Cloud login and a separate existing role for audit retention. The Cloud login must be distinct from the database and audit administrator. Connect to the Cloud database using an administrator that can create PostgreSQL event triggers. Leave the startup option unset during installation.
2. From the checkout that supplied the Cloud binary, install the audit objects with `psql`. Substitute the names of the existing roles and the administrator's connection service.

   ```sh
   psql --dbname='service=engram-audit-admin' \
     --set=runtime_role=engram_cloud_runtime \
     --set=retention_role=engram_receipt_retention \
     --file=scripts/cloud/install-startup-receipt.sql
   ```

   The script grants the Cloud login read access to receipt evidence and permission to record verification witnesses. It grants the retention role access to the maintenance functions. A missing role, ineligible source table, database lock timeout or default privilege outside those grants rolls back the installation.

3. In the same execution environment that will run Cloud, set `ENGRAM_DATABASE_URL`, `ENGRAM_CLOUD_ALLOWED_PROJECTS` and `ENGRAM_CLOUD_STARTUP_RECEIPT_DIR`. Run the preparation command using the runtime login.

   ```sh
   engram cloud repair prepare-startup-receipt
   ```

   The command verifies an existing PostgreSQL snapshot without changing application rows. It commits a witness in the audit schema in that same transaction before storing `startup-receipt-v3.json` in the private directory. If preparation reports incomplete coverage, start normally so Engram can run repair, then prepare again. A storage error calls for checking the space and write permissions of the receipt directory before retrying.

4. Restart `engram cloud serve` with the same settings. The log entry `protected receipt reused` confirms bounded startup. When scope or database enrollment changes, prepare a new receipt. A timeout, a missing witness after a physical restore, or a declined proof resumes ordinary startup repair. Quiet restarts reuse the existing witness. A verified append records a fresh witness before advancing the receipt. Retention prunes old witnesses with the audit history.

## Adopt a separate runtime from the Compose bootstrap

The bundled Compose bootstrap initially uses a database administrator as its
Cloud login. If that login owns the ordinary Cloud tables under `public`, an
administrator can hand them to a separately provisioned restricted login. Grant the new login
`CONNECT` on the Cloud database before running the script.
Stop Cloud before the handoff. From the checkout for this version, run the
following against the Cloud database.

```sh
psql -X --dbname='service=engram-audit-admin' \
  --set=previous_owner=engram \
  --set=runtime_role=engram_cloud_runtime \
  --file=scripts/cloud/adopt-startup-receipt-runtime.sql
```

The script locks and verifies the known Cloud tables before transferring
ownership, including their owned sequences. Leave the administrator's
connection available for audit maintenance. Set Cloud's connection to the
restricted runtime role and verify normal serving before installing the
startup audit.

## Refresh enrollment after a source schema change

A migration that changes a protected table's definition can make an existing enrollment ineligible for receipt reuse. For example, adding `cloud_chunks.submitted_by_principal_id` changes the fingerprint recorded at enrollment. Cloud starts with ordinary repair in that case, and preparing another receipt against the old enrollment fails.

After the upgrade has run its migration, follow [Remove enrollment](#remove-enrollment) and then [Install and prepare](#install-and-prepare) using the upgraded checkout. The new enrollment records the updated schema. Preparing a fresh receipt makes eligible restarts use the audit again.

## Remove enrollment

Unset `ENGRAM_CLOUD_STARTUP_RECEIPT_DIR` and restart Cloud normally. Use the administrator connection to remove the named audit objects. The operation runs as one transaction and fails if an unexpected dependency requires removal.

```sh
psql --dbname='service=engram-audit-admin' \
  --file=scripts/cloud/remove-startup-receipt.sql
```

The Cloud database retains its application rows, role registrations, and any ownership or grants from the runtime handoff. Any saved receipt belongs to its previous enrollment. A later enrollment requires preparing a fresh receipt before it can be reused.
