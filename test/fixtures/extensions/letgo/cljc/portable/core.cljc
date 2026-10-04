(ns portable.core
  "Pure logic shared by let-go and Babashka. No host APIs."
  (:require [clojure.string :as str]))

(def host #?(:lg :let-go :default :other))

(defn classify [text]
  (let [words (str/split (str/trim text) #"\s+")]
    (into (sorted-map)
          {:words (count words)
           :longest (reduce (fn [a b] (if (> (count b) (count a)) b a)) "" words)
           :upper (str/upper-case (str/trim text))})))

(defn spliced []
  [1 #?@(:lg [2 3] :default [2 3]) 4])

(defn report [text]
  (pr-str [(classify text)
           (spliced)
           (sort (keys {:b 1 :a 2 :c 3}))
           (mapv inc (range 3))
           (into (sorted-map) (frequencies (seq "abca")))
           (->> [5 3 9 1] (filter odd?) (map #(* % 10)) (reduce +))]))
