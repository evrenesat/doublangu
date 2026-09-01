import { base } from '$app/paths';

/** Prefix an application-root path with the configured SvelteKit base path. */
export function appPath(path: `/${string}`): string {
	return `${base}${path}`;
}

/** Remove the configured deployment base from a browser pathname. */
export function appRelativePath(pathname: string): `/${string}` {
	if (base && pathname.startsWith(`${base}/`)) return pathname.slice(base.length) as `/${string}`;
	return (pathname.startsWith('/') ? pathname : `/${pathname}`) as `/${string}`;
}
