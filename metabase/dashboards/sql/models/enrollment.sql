-- One row per student with their school, ready to use without writing a join.
SELECT st.student_id,
       s.school_id,
       s.school_name,
       s.school_type,
       st.date_enrolled,
       st.expected_graduation
FROM public.students st
JOIN public.schools s ON s.school_id = st.school_id
