// Capability negotiation between the page and the Android shell.
//
// The shell is a thin WebView host, so the page must run against several shell
// versions at once: an APK installed months ago still loads today's web assets.
// Instead of scattering "does window.PocketAndroidX exist" checks — which turn a
// missing bridge into a hard error like "please update the client" — the page asks
// what the shell can do and degrades around the gaps.

export const inNativeShell = /PocketAgentAndroid\//.test(navigator.userAgent);

// Feature names are stable strings, not booleans, so a shell can advertise a
// narrower surface than "the whole object exists".
export const Features = Object.freeze({
	deviceIdentity: 'device.identity',
	deviceSign: 'device.sign',
	authVault: 'auth',
	preferences: 'preferences',
	care: 'care',
	voice: 'voice',
	exports: 'exports',
	shell: 'shell',
	filePicker: 'files.picker',
});

let cached = null;

// bridgeOpened reports whether the injected script has actually run. The shell
// injects it on page finished, which is after the page's modules have evaluated,
// so "no bridge object yet" is a timing state and not a capability answer.
function bridgeOpened(version) {
	if (version > 0) return true;
	return Boolean(
		window.PocketAndroidDevice || window.PocketAndroidAuth ||
		window.PocketAndroidPreferences || window.PocketAndroidCare ||
		window.PocketAndroidVoice || window.PocketAndroidExports ||
		window.PocketAndroidShell
	);
}

// capabilities() returns { version, features }. version 0 means an older shell
// that only exposes objects; the feature list is then inferred from what exists.
//
// While the injected script has not run the result is deliberately not cached.
// Caching an empty list there would tell the page forever that this client cannot
// upload images, even after the bridge arrives a moment later.
export function capabilities() {
	if (cached) return cached;
	let version = 0;
	let features = [];
	if (typeof window.PocketAndroidCapabilities === 'function') {
		try {
			const reported = window.PocketAndroidCapabilities();
			version = Number(reported?.version) || 0;
			features = Array.isArray(reported?.features) ? reported.features.map(String) : [];
		} catch {
			version = 0;
			features = [];
		}
	}
	if (!version) {
		// Older shell: infer from the injected objects.
		if (window.PocketAndroidDevice) features.push(Features.deviceIdentity, Features.deviceSign);
		if (window.PocketAndroidAuth) features.push(Features.authVault);
		if (window.PocketAndroidPreferences) features.push(Features.preferences);
		if (window.PocketAndroidCare) features.push(Features.care);
		if (window.PocketAndroidVoice) features.push(Features.voice);
		if (window.PocketAndroidExports) features.push(Features.exports);
		if (window.PocketAndroidShell) features.push(Features.shell);
		// The WebView file chooser has existed since the first shell: any Android
		// host implements onShowFileChooser. Inferring it from the newer capability
		// list alone would tell older APKs they cannot upload, which is false.
		if (features.length) features.push(Features.filePicker);
	}
	if (inNativeShell && !bridgeOpened(version)) {
		return Object.freeze({ version: 0, features: Object.freeze([]), pending: true });
	}
	cached = { version, features: Object.freeze([...new Set(features)]) };
	return cached;
}

// supports() answers whether one capability is available right now.
export function supports(feature) {
	return capabilities().features.includes(feature);
}

// waitForShell resolves once the bridge has opened (or the timeout elapses), so
// callers do not have to race the injected script on first paint.
export function waitForShell(timeout = 2500) {
	if (!inNativeShell) return Promise.resolve(false);
	if (window.PocketAndroidDevice || window.PocketAndroidShell) return Promise.resolve(true);
	return new Promise(resolve => {
		const done = () => {
			clearTimeout(timer);
			window.removeEventListener('pocket-native-ready', done);
			resolve(Boolean(window.PocketAndroidDevice || window.PocketAndroidShell));
		};
		const timer = setTimeout(done, timeout);
		window.addEventListener('pocket-native-ready', done, { once: true });
	});
}

// describeGap() turns a missing feature into a sentence a person can act on,
// instead of an instruction to reinstall the app for something optional.
export function describeGap(feature) {
	switch (feature) {
		case Features.voice:
			return '这台客户端还不能语音输入。';
		case Features.care:
			return '这台客户端还不能发系统通知。';
		case Features.preferences:
			return '这台客户端还不能记住输入框偏好。';
		case Features.exports:
			return '这台客户端还不能直接保存文件。';
		case Features.filePicker:
			return '这台客户端还不能上传图片。';
		default:
			return '这台客户端缺少一项可选能力。';
	}
}
