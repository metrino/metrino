-- Courses and enrollments (CSVs in example-jdbc/data, copied to /data).
CREATE TABLE IF NOT EXISTS courses (
    "course_id" UUID PRIMARY KEY,
    "course_name" TEXT NOT NULL,
    "discipline" TEXT NOT NULL,
    "academic_type" TEXT NOT NULL
);

-- Schools, students, staff and departments (the same CSVs the example-http server
-- serves, copied to /data-http), so both sample sources describe the same people.
CREATE TABLE IF NOT EXISTS departments (
    "department_id" UUID PRIMARY KEY,
    "department_name" TEXT NOT NULL,
    "chief_officer_id" BIGINT
);

CREATE TABLE IF NOT EXISTS schools (
    "school_id" UUID PRIMARY KEY,
    "school_name" TEXT NOT NULL,
    "school_type" TEXT,
    -- Grades include non-numeric values such as 'PreK' and 'K', so these are text.
    "grade_min" TEXT,
    "grade_max" TEXT,
    "principal_id" BIGINT,
    "phone" TEXT,
    "address" TEXT,
    "website" TEXT
);

CREATE TABLE IF NOT EXISTS staff (
    "staff_id" BIGINT PRIMARY KEY,
    "first_name" TEXT NOT NULL,
    "last_name" TEXT NOT NULL,
    "school_id" UUID REFERENCES schools ("school_id"),
    "email" TEXT,
    "phone" TEXT,
    "department_id" UUID REFERENCES departments ("department_id")
);

CREATE TABLE IF NOT EXISTS students (
    "student_id" BIGINT PRIMARY KEY,
    "first_name" TEXT NOT NULL,
    "last_name" TEXT NOT NULL,
    "school_id" UUID REFERENCES schools ("school_id"),
    "date_enrolled" DATE,
    "expected_graduation" DATE,
    "email" TEXT
);

CREATE TABLE IF NOT EXISTS student_courses (
    "course_id" UUID REFERENCES courses ("course_id"),
    "student_id" BIGINT NOT NULL REFERENCES students ("student_id"),
    "status" TEXT NOT NULL,
    "grade" TEXT NOT NULL
);

-- Parent tables are loaded before the tables that reference them.
COPY courses FROM '/data/courses.csv' WITH (FORMAT csv, HEADER true);
COPY departments FROM '/data-http/departments.csv' WITH (FORMAT csv, HEADER true);
COPY schools FROM '/data-http/schools.csv' WITH (FORMAT csv, HEADER true);
COPY staff FROM '/data-http/staff.csv' WITH (FORMAT csv, HEADER true);
COPY students FROM '/data-http/students.csv' WITH (FORMAT csv, HEADER true);
COPY student_courses FROM '/data/student_courses.csv' WITH (FORMAT csv, HEADER true);
