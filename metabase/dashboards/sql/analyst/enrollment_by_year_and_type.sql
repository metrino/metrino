-- The year is cast to text so Metabase shows 2018, not 2,018.
-- Narrowed by the dashboard filters School and School type, when they are set.
SELECT CAST(year(st.date_enrolled) AS varchar) AS enrollment_year,
       s.school_type AS school_type,
       count(*) AS students
FROM public.students st
JOIN public.schools s ON s.school_id = st.school_id
WHERE 1 = 1
  [[AND s.school_name = {{school}}]]
  [[AND s.school_type = {{school_type}}]]
GROUP BY 1, 2
ORDER BY 1, 2
