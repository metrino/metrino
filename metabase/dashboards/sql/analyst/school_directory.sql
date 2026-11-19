-- Narrowed by the dashboard filters School and School type, when they are set.
SELECT school_name,
       school_type,
       grade_min,
       grade_max,
       phone,
       website
FROM public.schools
WHERE 1 = 1
  [[AND school_name = {{school}}]]
  [[AND school_type = {{school_type}}]]
ORDER BY school_name
