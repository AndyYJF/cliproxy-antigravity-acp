const result = await Bun.build({entrypoints: ['management-center/shared/index.ts'], target: 'browser', format: 'esm', minify: true});
if (!result.success) throw new Error(String(result.logs));
const license = await Bun.file('management-center/shared/LICENSE').text();
await Bun.write('cmd/plugin/web/account-helpers.js', '/*! CPA Management Center shared helpers\n' + license + '*/\n' + await result.outputs[0].text());
