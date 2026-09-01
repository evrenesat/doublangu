import { base } from '$app/paths';

function normalizedBasePath(value: string): string {
	if (!value || value === '/') return '';
	return `/${value.replace(/^\/+|\/+$/g, '')}`;
}

/** Prefix a same-origin application path without double-prefixing a base. */
export function withAppBasePath(path: string, basePath = base): string {
	if (!path.startsWith('/') || path.startsWith('//')) return path;
	const prefix = normalizedBasePath(basePath);
	if (!prefix || path === prefix || path.startsWith(`${prefix}/`)) return path;
	return `${prefix}${path}`;
}

/** Prefix an application-root path with the configured SvelteKit base path. */
export function appPath(path: `/${string}`): string {
	return withAppBasePath(path);
}

/** Prefix an API-provided same-origin audio URL with the web base path. */
export function appAudioPath(path: string, basePath = base): string {
	return withAppBasePath(path, basePath);
}

/** Remove the configured deployment base from a browser pathname. */
export function appRelativePath(pathname: string): `/${string}` {
	if (base && pathname.startsWith(`${base}/`)) return pathname.slice(base.length) as `/${string}`;
	return (pathname.startsWith('/') ? pathname : `/${pathname}`) as `/${string}`;
}
