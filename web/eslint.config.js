import globals from "globals";
import react from "eslint-plugin-react";

export default [{
  files: ["src/**/*.jsx"],
  languageOptions: {
    ecmaVersion: "latest",
    sourceType: "module",
    parserOptions: { ecmaFeatures: { jsx: true } },
    globals: globals.browser,
  },
  plugins: { react },
  rules: {
    "no-undef": "error",
    "no-unused-vars": ["error", { args: "none", ignoreRestSiblings: true }],
    "react/jsx-no-undef": "error",
    "react/jsx-uses-vars": "error",
    "react/jsx-uses-react": "error",
  },
}];
