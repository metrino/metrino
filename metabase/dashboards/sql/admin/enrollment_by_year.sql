-- The year is cast to text so Metabase shows 2018, not 2,018.
SELECT CAST(year(date_enrolled) AS varchar) AS enrollment_year,
       count(*) AS students
FROM public.students
GROUP BY 1
ORDER BY 1
