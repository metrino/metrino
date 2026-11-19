SELECT s.school_name AS school,
       count(*) AS students
FROM public.students st
JOIN public.schools s ON s.school_id = st.school_id
GROUP BY s.school_name
ORDER BY students DESC, school
LIMIT 10
