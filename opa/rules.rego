package trino

# Who can read which data in the compose stack's sample data. Edit this file to change access locally.
# helm/opa/policy.rego applies these rules and doesn't need changing.
#
# Each key names a catalog, schema, table or column ("catalog.schema.table.column") and lists the Metabase
# groups whose members may read it. Group names must match Metabase's exactly.
#   - The most specific key wins: a table's own key, otherwise its schema's, otherwise its catalog's.
#   - Anything no key covers can only be read by Administrators, who can read everything.
#   - A column someone can't read comes back as NULL. To read a column they also need its table.
#   - Being in any one of the listed groups is enough.
#   - No key can name a schema, table or column with a "." in its name, so only Administrators can read one.
# After a change, update rules_test.rego, next to this file, which states what these rules promise.
rules := {
	# Analysts read all of the sample data.
	"example_jdbc": ["Analysts"],
	"openapi": ["Analysts"],
	# Teachers read the district's directory and course tables. They're listed table by table, so a new table
	# isn't visible to Teachers until it's added here. Without row-level security a teacher sees every
	# student in the district, so student names, contact details and grades, and staff phone numbers, are
	# hidden from them. Teachers can't read openapi, which serves the same student and staff data over HTTP.
	"example_jdbc.public.schools": ["Analysts", "Teachers"],
	"example_jdbc.public.courses": ["Analysts", "Teachers"],
	"example_jdbc.public.departments": ["Analysts", "Teachers"],
	"example_jdbc.public.staff": ["Analysts", "Teachers"],
	"example_jdbc.public.staff.phone": ["Analysts"],
	"example_jdbc.public.students": ["Analysts", "Teachers"],
	"example_jdbc.public.students.first_name": ["Analysts"],
	"example_jdbc.public.students.last_name": ["Analysts"],
	"example_jdbc.public.students.email": ["Analysts"],
	"example_jdbc.public.student_courses": ["Analysts", "Teachers"],
	"example_jdbc.public.student_courses.grade": ["Analysts"],
}
