package system.authz_test

import data.system.authz

# What the web server asks of its OPA: decisions, and reading documents.
test_post_data_allowed if authz.allow with input as {"method": "POST", "path": ["v1", "data", "trino", "allow"]}

test_post_batch_column_masks_allowed if authz.allow with input as {"method": "POST", "path": ["v1", "data", "trino", "batchColumnMasks"]}

test_get_data_allowed if authz.allow with input as {"method": "GET", "path": ["v1", "data", "trino", "rules"]}

# Anything that would replace the policy or its data, or run arbitrary queries.
test_put_policy_denied if not authz.allow with input as {"method": "PUT", "path": ["v1", "policies", "policy", "policy.rego"]}

test_delete_policy_denied if not authz.allow with input as {"method": "DELETE", "path": ["v1", "policies", "x"]}

test_get_policies_denied if not authz.allow with input as {"method": "GET", "path": ["v1", "policies"]}

test_put_data_denied if not authz.allow with input as {"method": "PUT", "path": ["v1", "data", "trino", "x"]}

test_patch_data_denied if not authz.allow with input as {"method": "PATCH", "path": ["v1", "data", "trino"]}

test_delete_data_denied if not authz.allow with input as {"method": "DELETE", "path": ["v1", "data", "trino"]}

test_post_query_denied if not authz.allow with input as {"method": "POST", "path": ["v1", "query"]}

test_post_compile_denied if not authz.allow with input as {"method": "POST", "path": ["v1", "compile"]}

test_get_health_denied if not authz.allow with input as {"method": "GET", "path": ["health"]}
