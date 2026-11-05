const PUBLIC_AUTH_PATHS = [
	'/login',
	'/register',
	'/verify-email',
	'/forgot-password',
	'/reset-password',
];

export function shouldRedirectToLogin(pathname: string): boolean {
	return !PUBLIC_AUTH_PATHS.some(
		(path) => pathname === path || pathname.startsWith(`${path}/`),
	);
}

export function buildLoginSearch(
	pathname: string,
	search: string,
): { redirect?: string } {
	return shouldRedirectToLogin(pathname)
		? { redirect: `${pathname}${search}` }
		: {};
}
