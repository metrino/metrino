-- School contact details are public. principal_id (a staff id) is left out.
SELECT school_id,
       school_name,
       school_type,
       grade_min,
       grade_max,
       address,
       phone,
       website
FROM public.schools
