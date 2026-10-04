package main

import (
	"fmt"
	"strings"
)

// letGoInitLanguage is the --lang value for interpreted let-go sources.
const letGoInitLanguage = "let-go"

// letGoScaffoldFiles builds the let-go scaffold. The default is portable .cljc: a thin adapter that requires the host through the one
// reader conditional, over a pure core that touches no host API, so the same source can load on Kmet. Only an explicit request produces
// let-go-specific .lg source. Neither form has a manifest: the entry file and the directory name are the whole contract.
//
// pig additive (D89): there is no SDK, build step or toolchain to scaffold; Pig interprets the source in process.
func letGoScaffoldFiles(name string, letGoSource bool) []scaffoldFile {
	if letGoSource {
		return []scaffoldFile{{rel: "extension.lg", body: letGoSourceTemplate(name)}}
	}
	return []scaffoldFile{
		{rel: "extension.cljc", body: letGoPortableTemplate(name)},
		{rel: letGoCorePath(name), body: letGoCoreTemplate(name)},
	}
}

// letGoCorePath is the helper namespace's file under the extension directory: hyphens in a namespace segment become underscores in its path.
func letGoCorePath(name string) string {
	return strings.ReplaceAll(name, "-", "_") + "/core.cljc"
}

// letGoNamespace makes the extension name a valid namespace prefix.
func letGoNamespace(name string) string {
	if name == "" || name[0] >= '0' && name[0] <= '9' {
		return "ext-" + name
	}
	return name
}

func letGoPortableTemplate(name string) string {
	namespace := letGoNamespace(name)
	return fmt.Sprintf(`(ns %[2]s.extension
  (:require #?(:lg [pig.extension :as ext]
               :default [kmet.extension :as ext])
            [%[2]s.core :as core]))

;; A thin adapter over a pure core. Keep host calls here and logic in %[2]s.core, which any host can load.

(defn init [api]
  (ext/register-tool!
   api
   {:name "%[1]s_ping"
    :description "Return a short pong so you can confirm the extension is wired up."
    :parameters {:type "object"
                 :properties {:text {:type "string"}}}
    :execute (fn [args]
               {:content (core/pong (:text args))})})

  (ext/register-command!
   api
   {:name "%[1]s"
    :description "Say hello from the %[1]s extension."
    :handler (fn [ctx args]
               (let [message (core/greeting args)]
                 ;; The host call at the edge is the one place the hosts differ.
                 #?(:lg ((:notify ctx) message :info)
                    :default (ext/ui-notify api message :info))))}))

(defn shutdown [_api]
  nil)
`, name, namespace)
}

func letGoCoreTemplate(name string) string {
	namespace := letGoNamespace(name)
	return fmt.Sprintf(`(ns %[2]s.core
  "Pure logic shared by every host. No host API belongs in this namespace."
  (:require [clojure.string :as str]))

(defn pong [text]
  (if (str/blank? text)
    "pong from %[1]s"
    (str "pong from %[1]s: " text)))

(defn greeting [args]
  (if (str/blank? args)
    "hello from %[1]s"
    (str "hello from %[1]s, " (str/trim args))))
`, name, namespace)
}

func letGoSourceTemplate(name string) string {
	namespace := letGoNamespace(name)
	return fmt.Sprintf(`(ns %[2]s.extension
  (:require [pig.extension :as ext]))

;; let-go-specific source: it loads only on Pig. Use extension.cljc from "pig extension init --lang let-go"
;; when the extension should stay portable.

(defn init [api]
  (ext/register-tool!
   api
   {:name "%[1]s_ping"
    :description "Return a short pong so you can confirm the extension is wired up."
    :parameters {:type "object"
                 :properties {:text {:type "string"}}}
    :execute (fn [args]
               {:content (str "pong from %[1]s" (when-let [text (:text args)] (str ": " text)))})})

  (ext/register-command!
   api
   {:name "%[1]s"
    :description "Say hello from the %[1]s extension."
    :handler (fn [ctx args]
               ((:notify ctx) "hello from %[1]s" :info))}))

(defn shutdown [_api]
  nil)
`, name, namespace)
}
