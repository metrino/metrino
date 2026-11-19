SELECT d.department_name AS department,
       count(*) AS staff
FROM public.staff sf
JOIN public.departments d ON d.department_id = sf.department_id
GROUP BY d.department_name
ORDER BY staff DESC, department
