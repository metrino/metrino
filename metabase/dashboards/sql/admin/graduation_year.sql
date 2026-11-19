-- The year is cast to text so Metabase shows 2030, not 2,030.
SELECT CAST(year(expected_graduation) AS varchar) AS graduation_year,
       count(*) AS students
FROM public.students
GROUP BY 1
ORDER BY 1
