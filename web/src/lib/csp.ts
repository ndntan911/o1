import { z } from 'zod';

// The Content-Security-Policy forbids eval. Zod 4 compiles parsers with
// `new Function` by default; jitless keeps every schema working under the
// policy (see the header in web/Caddyfile).
z.config({ jitless: true });
