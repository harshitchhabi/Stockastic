/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  transpilePackages: ["@stockastic/config", "@stockastic/matching-engine"],
};

module.exports = nextConfig;
