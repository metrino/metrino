package trino_test

# What the real rules (rules.rego) promise about the sample data. Written by hand: a change to rules.rego that
# lets a group see more must change a test here too, so it shows up in review.

jdbc_table(name) := table("example_jdbc", "public", name)

jdbc_column(table_name, name) := column("example_jdbc", "public", table_name, name, "varchar")

district_tables := ["schools", "courses", "departments", "staff", "students", "student_courses"]

test_teachers_read_the_district_tables if {
	every name in district_tables {
		can_select(teacher, jdbc_table(name))
	}
}

# Teachers are granted table by table, so a new table isn't visible to them until someone adds it.
test_teachers_cannot_read_a_new_table if {
	not can_select(teacher, jdbc_table("new_table"))
}

test_teachers_cannot_read_openapi if {
	every name in ["students_", "students_student_id", "staff_", "schools_", "departments_"] {
		not can_select(teacher, table("openapi", "default", name))
	}
}

test_teachers_do_not_see_student_names_contact_details_or_grades if {
	hidden(teacher, jdbc_column("students", "first_name"))
	hidden(teacher, jdbc_column("students", "last_name"))
	hidden(teacher, jdbc_column("students", "email"))
	hidden(teacher, jdbc_column("student_courses", "grade"))
}

test_teachers_do_not_see_staff_phone_numbers if {
	hidden(teacher, jdbc_column("staff", "phone"))
}

test_teachers_see_the_other_columns if {
	visible(teacher, jdbc_column("students", "student_id"))
	visible(teacher, jdbc_column("students", "school_id"))
	visible(teacher, jdbc_column("student_courses", "status"))
	visible(teacher, jdbc_column("staff", "email"))
	visible(teacher, jdbc_column("schools", "phone"))
}

test_analysts_read_all_the_sample_data if {
	every name in array.concat(district_tables, ["new_table"]) {
		can_select(analyst, jdbc_table(name))
	}
	can_select(analyst, table("openapi", "default", "students_"))
	visible(analyst, jdbc_column("students", "first_name"))
	visible(analyst, jdbc_column("student_courses", "grade"))
}

test_analysts_cannot_read_catalogs_without_a_rule if {
	not can_select(analyst, table("tpch", "tiny", "nation"))
}

test_admins_read_everything if {
	can_select(admin, table("tpch", "tiny", "nation"))
	visible(admin, jdbc_column("students", "first_name"))
}
