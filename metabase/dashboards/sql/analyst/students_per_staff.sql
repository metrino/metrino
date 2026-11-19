-- Narrowed by the dashboard filters School and School type, when they are set.
SELECT s.school_name AS school,
       st.students AS students,
       sf.staff AS staff,
       round(CAST(st.students AS double) / sf.staff, 1) AS students_per_staff
FROM public.schools s
JOIN (SELECT school_id, count(*) AS students
      FROM public.students GROUP BY school_id) st ON st.school_id = s.school_id
JOIN (SELECT school_id, count(*) AS staff
      FROM public.staff GROUP BY school_id) sf ON sf.school_id = s.school_id
WHERE 1 = 1
  [[AND s.school_name = {{school}}]]
  [[AND s.school_type = {{school_type}}]]
ORDER BY students_per_staff DESC, school
