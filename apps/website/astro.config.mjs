import { defineConfig } from 'astro/config';
import react    from '@astrojs/react';
import tailwind from '@astrojs/tailwind';
import sitemap  from '@astrojs/sitemap';

export default defineConfig({
  site: 'https://forge-ci.dev',
  integrations: [
    react(),
    tailwind({ applyBaseStyles: false }),
    sitemap(),
  ],
  output: 'static',
  build: { assets: '_forge' },
  vite: {
    ssr: { noExternal: ['@xyflow/react','recharts','d3'] },
    define: {
      'process.env.KRATOS_PUBLIC_URL': JSON.stringify(process.env.KRATOS_PUBLIC_URL ?? ''),
      'process.env.HYDRA_PUBLIC_URL':  JSON.stringify(process.env.HYDRA_PUBLIC_URL  ?? ''),
      'process.env.KETO_READ_URL':     JSON.stringify(process.env.KETO_READ_URL     ?? ''),
    },
  },
});
