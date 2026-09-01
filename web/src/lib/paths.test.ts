import { expect, it } from 'vitest';
import { appAudioPath, withAppBasePath } from './paths';

it('prefixes browser audio URLs under /beta without double-prefixing', () => {
	expect(appAudioPath('/api/v1/audio/render-id')).toBe('/api/v1/audio/render-id');
	expect(appAudioPath('/api/v1/audio/render-id', '/beta')).toBe('/beta/api/v1/audio/render-id');
	expect(appAudioPath('/beta/api/v1/audio/render-id', '/beta')).toBe('/beta/api/v1/audio/render-id');
	expect(withAppBasePath('https://audio.example/render-id', '/beta')).toBe('https://audio.example/render-id');
});
