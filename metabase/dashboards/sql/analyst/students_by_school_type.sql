-- Narrowed by the dashboard filters School and School type, when they are set.
SELECT s.school_type AS school_type,
       count(*) AS students
FROM public.students st
JOIN public.schools s ON s.school_id = st.school_id
WHERE 1 = 1
  [[AND s.school_name = {{school}}]]
  [[AND s.school_type = {{school_type}}]]
GROUP BY 1
ORDER BY students DESC, school_type
